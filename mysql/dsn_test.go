package mysql

import (
	"strings"
	"testing"

	mysqldriver "github.com/go-sql-driver/mysql"
)

func TestParseAdminDSN(t *testing.T) {
	tests := map[string]struct {
		raw     string
		user    string
		pass    string
		addr    string
		dbName  string
		params  map[string]string
		wantErr bool
	}{
		"native form": {
			raw:  "root:secret@tcp(db.example:3307)/admin?parseTime=true",
			user: "root", pass: "secret", addr: "db.example:3307", dbName: "admin",
		},
		"mysql URL": {
			// parseTime is a known parameter the driver stores in a field;
			// unknown ones like custom_param land in Params. Both must survive.
			raw:  "mysql://root:secret@db.example:3307/admin?parseTime=true&custom_param=kept",
			user: "root", pass: "secret", addr: "db.example:3307", dbName: "admin",
			params: map[string]string{"custom_param": "kept"},
		},
		"mysql URL without port or database": {
			raw:  "mysql://root@localhost/",
			user: "root", addr: "localhost:3306",
		},
		"mysql URL without host": {
			raw:     "mysql:///admin",
			wantErr: true,
		},
		"garbage": {
			raw:     "host=localhost dbname=x",
			wantErr: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			config, err := parseAdminDSN(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got config %+v", config)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseAdminDSN(%q): %v", tc.raw, err)
			}
			if config.User != tc.user || config.Passwd != tc.pass || config.Addr != tc.addr || config.DBName != tc.dbName {
				t.Errorf("got user=%q pass=%q addr=%q db=%q, want user=%q pass=%q addr=%q db=%q",
					config.User, config.Passwd, config.Addr, config.DBName, tc.user, tc.pass, tc.addr, tc.dbName)
			}
			if config.Net != "tcp" {
				t.Errorf("Net = %q, want tcp", config.Net)
			}
			for k, v := range tc.params {
				if config.Params[k] != v {
					t.Errorf("param %s = %q, want %q", k, config.Params[k], v)
				}
			}
		})
	}
}

func TestBuildDSNCarriesAdminSettings(t *testing.T) {
	p := &Provider{}
	config, err := parseAdminDSN("mysql://root:secret@localhost:3306/?parseTime=true&timeout=3s&tls=skip-verify")
	if err != nil {
		t.Fatal(err)
	}
	p.adminConfig = config

	dsn, err := p.BuildDSN("test_123_abcd")
	if err != nil {
		t.Fatal(err)
	}
	got, err := mysqldriver.ParseDSN(dsn)
	if err != nil {
		t.Fatalf("BuildDSN produced an unparsable DSN %q: %v", dsn, err)
	}
	if got.DBName != "test_123_abcd" {
		t.Errorf("DBName = %q", got.DBName)
	}
	if !got.ParseTime || got.Timeout.String() != "3s" || got.TLSConfig != "skip-verify" {
		t.Errorf("admin settings were not carried over: parseTime=%v timeout=%s tls=%q", got.ParseTime, got.Timeout, got.TLSConfig)
	}
	if got.User != "root" || got.Passwd != "secret" || got.Addr != "localhost:3306" {
		t.Errorf("credentials or address changed: %+v", got)
	}
	// The admin config itself is untouched
	if p.adminConfig.DBName != "" {
		t.Errorf("BuildDSN modified the admin config: DBName = %q", p.adminConfig.DBName)
	}
}

func TestBuildDSNBeforeInitialize(t *testing.T) {
	if _, err := (&Provider{}).BuildDSN("x"); err == nil {
		t.Fatal("expected an error before Initialize")
	}
}

func TestQuoteIdentifier(t *testing.T) {
	if got := quoteIdentifier("plain"); got != "`plain`" {
		t.Errorf("got %s", got)
	}
	if got := quoteIdentifier("with`tick"); got != "`with``tick`" {
		t.Errorf("got %s", got)
	}
}

func TestInspectCommand(t *testing.T) {
	p := &Provider{}
	got := p.InspectCommand("root:p'w@tcp(db.example:3307)/test_1_a?parseTime=true")
	for _, want := range []string{"mysql ", "--host='db.example'", "--port=3307", "--user='root'", `--password='p'\''w'`, "'test_1_a'"} {
		if !strings.Contains(got, want) {
			t.Errorf("InspectCommand = %q, missing %q", got, want)
		}
	}
	if got := p.InspectCommand("not a dsn"); got != "not a dsn" {
		t.Errorf("an unparsable DSN should be shown as is, got %q", got)
	}
	unix := p.InspectCommand("root@unix(/var/run/mysqld/mysqld.sock)/db")
	if !strings.Contains(unix, "--socket='/var/run/mysqld/mysqld.sock'") {
		t.Errorf("unix socket DSN should use --socket, got %q", unix)
	}
}
