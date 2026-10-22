package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/bashhack/testdb"
)

// readyPool keeps clones of a template prepared ahead of demand, so a test
// pays a channel receive instead of a clone. Several workers clone in
// parallel, because a clone's cost is the server's per-table DDL and the
// server handles concurrent clones well; take never waits for them.
//
// Accounting is exact: size = slots free + clones in flight + names ready.
// A worker takes a slot before cloning; take hands the slot back.
//
// Workers do their bookkeeping through a *sql.DB rather than a pinned
// connection: when Release cancels a statement in flight, database/sql
// discards that connection, and the clean-up that follows must still get
// a working one.
type readyPool struct {
	tpl    *Template
	db     *sql.DB
	prefix string
	ready  chan string
	slots  chan struct{}
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu      sync.Mutex
	failure error // the first refill error; after it, no more refills
}

func startReadyPool(tpl *Template, size, workers int) (*readyPool, error) {
	db, err := sql.Open("mysql", tpl.provider.adminDSN)
	if err != nil {
		return nil, fmt.Errorf("open pool connection: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	pool := &readyPool{
		tpl:    tpl,
		db:     db,
		prefix: tpl.readyPrefix,
		ready:  make(chan string, size),
		slots:  make(chan struct{}, size),
		ctx:    ctx,
		cancel: cancel,
	}
	for range size {
		pool.slots <- struct{}{}
	}
	for range min(workers, size) {
		pool.wg.Go(pool.work)
	}
	return pool, nil
}

// waitFull blocks until the pool holds its full number of prepared
// databases, the workers have given up, or ctx ends.
func (p *readyPool) waitFull(ctx context.Context) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if len(p.ready) == cap(p.ready) {
			return nil
		}
		p.mu.Lock()
		failed := p.failure != nil
		p.mu.Unlock()
		if failed {
			return nil // already logged; tests will clone on demand
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// take returns a prepared database, or false without waiting.
func (p *readyPool) take() (string, bool) {
	select {
	case name := <-p.ready:
		p.slots <- struct{}{} // never blocks: a name taken is a slot freed
		return name, true
	default:
		return "", false
	}
}

func (p *readyPool) work() {
	for {
		select {
		case <-p.slots:
		case <-p.ctx.Done():
			return
		}
		name, err := p.prepare()
		if err != nil {
			if p.ctx.Err() != nil {
				return
			}
			// Tests keep working with on-demand clones; say so once
			p.mu.Lock()
			first := p.failure == nil
			p.failure = err
			p.mu.Unlock()
			if first {
				log.Printf("testdb: template %q: preparing databases ahead failed, tests will clone on demand: %v", p.tpl.name, err)
			}
			return
		}
		select {
		case p.ready <- name:
		case <-p.ctx.Done():
			// Nobody will take it; drop it before leaving
			if err := p.drop(name); err != nil {
				log.Printf("testdb: %v", err)
			}
			return
		}
	}
}

// prepare records a new database in _testdb_ready first and clones the
// template into it second, so a clone cut short (by cancellation or a
// crash) is on the books and gets dropped by Release or swept as a dead
// process's leftover.
func (p *readyPool) prepare() (string, error) {
	name, err := testdb.NewDatabaseName(p.prefix)
	if err != nil {
		return "", err
	}
	_, err = p.db.ExecContext(p.ctx,
		fmt.Sprintf("INSERT INTO %s (db_name, connection_id, server_started_at) VALUES (?, ?, ?)",
			qualify(p.tpl.name, bookkeepingPrefix+"ready")), name, p.tpl.connID, p.tpl.serverStart)
	if err != nil {
		return "", fmt.Errorf("record prepared database: %w", err)
	}
	if err := cloneDatabase(p.ctx, p.tpl.provider.adminDSN, p.tpl.name, name); err != nil {
		return "", errors.Join(err, p.drop(name))
	}
	return name, nil
}

// drop removes a prepared database and its record, whichever of the two
// exist. It does not depend on the pool's context, so it works during
// shutdown.
func (p *readyPool) drop(name string) error {
	ctx := context.Background()
	if _, err := p.db.ExecContext(ctx, "DROP DATABASE IF EXISTS "+quoteIdentifier(name)); err != nil {
		return fmt.Errorf("drop prepared database %s: %w", name, err)
	}
	if _, err := p.db.ExecContext(ctx, "DELETE FROM "+qualify(p.tpl.name, bookkeepingPrefix+"ready")+" WHERE db_name = ?", name); err != nil {
		return fmt.Errorf("forget prepared database %s: %w", name, err)
	}
	return nil
}

// stop ends the workers and drops the prepared databases nobody took.
// Anything a worker had recorded but not finished is dropped by the
// template's Release through the bookkeeping table.
func (p *readyPool) stop() error {
	p.cancel()
	p.wg.Wait()

	var errs []error
	close(p.ready)
	for name := range p.ready {
		errs = append(errs, p.drop(name))
	}
	errs = append(errs, p.db.Close())
	p.mu.Lock()
	failure := p.failure
	p.mu.Unlock()
	if failure != nil {
		errs = append(errs, fmt.Errorf("preparing databases ahead had failed: %w", failure))
	}
	return errors.Join(errs...)
}
