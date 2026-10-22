package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"sync"

	"github.com/bashhack/testdb"
)

// readyPool keeps clones of a template prepared ahead of demand, so a test
// pays a channel receive instead of a clone. One refiller goroutine clones
// whenever the pool is below its size; take never waits for it.
//
// The refiller works through a *sql.DB rather than a pinned connection:
// when Release cancels a statement in flight, database/sql discards that
// connection, and the clean-up that follows must still get a working one.
type readyPool struct {
	tpl      *Template
	db       *sql.DB
	prefix   string
	ready    chan string
	wake     chan struct{}
	stopping chan struct{}
	done     chan struct{}
	cancel   context.CancelFunc

	mu      sync.Mutex
	failure error // the first refill error; after it, no more refills
}

func startReadyPool(tpl *Template, size int, prefix string) (*readyPool, error) {
	db, err := sql.Open("mysql", tpl.provider.adminDSN)
	if err != nil {
		return nil, fmt.Errorf("open pool connection: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	pool := &readyPool{
		tpl:      tpl,
		db:       db,
		prefix:   prefix,
		ready:    make(chan string, size),
		wake:     make(chan struct{}, 1),
		stopping: make(chan struct{}),
		done:     make(chan struct{}),
		cancel:   cancel,
	}
	go pool.refill(ctx)
	return pool, nil
}

// take returns a prepared database, or false without waiting.
func (p *readyPool) take() (string, bool) {
	select {
	case name := <-p.ready:
		select {
		case p.wake <- struct{}{}:
		default:
		}
		return name, true
	default:
		return "", false
	}
}

func (p *readyPool) refill(ctx context.Context) {
	defer close(p.done)
	for {
		for len(p.ready) < cap(p.ready) {
			name, err := p.prepare(ctx)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				// Tests keep working with synchronous clones; say so once
				p.mu.Lock()
				p.failure = err
				p.mu.Unlock()
				log.Printf("testdb: template %q: preparing databases ahead failed, tests will clone on demand: %v", p.tpl.name, err)
				return
			}
			select {
			case p.ready <- name:
			case <-p.stopping:
				// Nobody will take it; drop it before leaving
				if err := p.drop(name); err != nil {
					log.Printf("testdb: %v", err)
				}
				return
			}
		}
		select {
		case <-p.wake:
		case <-p.stopping:
			return
		}
	}
}

// prepare clones the template into a new database and records it. If
// recording fails, the clone is dropped so nothing is left unaccounted for.
func (p *readyPool) prepare(ctx context.Context) (string, error) {
	name, err := testdb.NewDatabaseName(p.prefix)
	if err != nil {
		return "", err
	}
	if err := cloneDatabase(ctx, p.tpl.provider.adminDSN, p.tpl.name, name); err != nil {
		return "", err
	}
	_, err = p.db.ExecContext(ctx,
		fmt.Sprintf("INSERT INTO %s (db_name, connection_id, server_started_at) VALUES (?, ?, ?)",
			qualify(p.tpl.name, bookkeepingPrefix+"ready")), name, p.tpl.connID, p.tpl.serverStart)
	if err != nil {
		return "", errors.Join(fmt.Errorf("record prepared database: %w", err), p.drop(name))
	}
	return name, nil
}

// drop removes a prepared database and its record, whichever of the two
// exist. It does not depend on the caller's context, so it works during
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

// stop ends refilling and drops the prepared databases nobody took.
func (p *readyPool) stop() error {
	close(p.stopping)
	p.cancel()
	<-p.done

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
