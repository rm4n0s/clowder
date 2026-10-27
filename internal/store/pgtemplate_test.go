package store

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/rm4n0s/clowder/internal/clock"
)

// unreachableTemplate is a PostgresTemplate whose administration pool points
// at a port where nothing listens, so that every statement of it fails without
// a server.
func unreachableTemplate(t *testing.T) *PostgresTemplate {
	t.Helper()
	const databaseURL = "postgres://clowder:hunter2@127.0.0.1:1/postgres?sslmode=disable&connect_timeout=2"
	cfg, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	base, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	admin := stdlib.OpenDB(*cfg)
	t.Cleanup(func() { _ = admin.Close() })
	return &PostgresTemplate{admin: admin, base: base, name: "clowder_tmpl_1_00000000"}
}

func TestPostgresTemplateFailures(t *testing.T) {
	t.Parallel()
	const password = "hunter2"
	tests := []struct {
		name  string
		run   func(t *testing.T) error
		route string
	}{
		{
			name: "the URL is not one of PostgreSQL",
			run: func(t *testing.T) error {
				_, err := NewPostgresTemplate(t.Context(), "mysql://clowder:"+password+"@127.0.0.1:3306/clowder")
				return err
			},
			route: "NewPostgresTemplate.TemplateURLInvalid",
		},
		{
			name: "the URL cannot be parsed",
			run: func(t *testing.T) error {
				_, err := NewPostgresTemplate(t.Context(), "postgres://clowder:"+password+"@127.0.0.1:5432/clowder?connect_timeout=x")
				return err
			},
			route: "NewPostgresTemplate.TemplateURLInvalid",
		},
		{
			name: "the server cannot be reached",
			run: func(t *testing.T) error {
				_, err := NewPostgresTemplate(t.Context(), "postgres://clowder:"+password+"@127.0.0.1:1/postgres?sslmode=disable&connect_timeout=2")
				return err
			},
			route: "NewPostgresTemplate.TemplateServerFailed",
		},
		{
			name: "NewPostgresDatabase: the URL is not one of PostgreSQL",
			run: func(t *testing.T) error {
				_, _, err := NewPostgresDatabase(t.Context(), "mysql://clowder:"+password+"@127.0.0.1:3306/clowder")
				return err
			},
			route: "NewPostgresDatabase.TemplateURLInvalid",
		},
		{
			name: "NewPostgresDatabase: the URL cannot be parsed",
			run: func(t *testing.T) error {
				_, _, err := NewPostgresDatabase(t.Context(), "postgres://clowder:"+password+"@127.0.0.1:5432/clowder?connect_timeout=x")
				return err
			},
			route: "NewPostgresDatabase.TemplateURLInvalid",
		},
		{
			name: "NewPostgresDatabase: the server cannot be reached",
			run: func(t *testing.T) error {
				_, _, err := NewPostgresDatabase(t.Context(), "postgres://clowder:"+password+"@127.0.0.1:1/postgres?sslmode=disable&connect_timeout=2")
				return err
			},
			route: "NewPostgresDatabase.TemplateServerFailed",
		},
		{
			name: "the copy cannot be made",
			run: func(t *testing.T) error {
				_, _, err := unreachableTemplate(t).Open(t.Context(), clock.NewFake(fakeStart), nil)
				return err
			},
			route: "PostgresTemplate.Open->PostgresTemplate.createDatabase.TemplateCreateFailed",
		},
		{
			name: "the database cannot be dropped",
			run: func(t *testing.T) error {
				return unreachableTemplate(t).dropDatabase(t.Context(), "clowder_t_1_00000000")
			},
			route: "PostgresTemplate.dropDatabase.TemplateDropFailed",
		},
		{
			name: "the leftovers cannot be listed",
			run: func(t *testing.T) error {
				return unreachableTemplate(t).sweep(t.Context())
			},
			route: "PostgresTemplate.sweep.TemplateSweepFailed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.run(t)
			if err == nil {
				t.Fatal("no error")
			}
			appErr := requireRoute(t, err, tt.route)
			for _, text := range []string{
				appErr.Message, appErr.Error(), fmt.Sprint(appErr.Metadata), fmt.Sprintf("%+v", appErr.ToJson()),
			} {
				if strings.Contains(text, password) {
					t.Errorf("the error leaks the password: %s", text)
				}
			}
		})
	}
}

func TestDatabaseNames(t *testing.T) {
	t.Parallel()
	now := time.Now()
	tests := []struct {
		name   string
		in     string
		wantAt time.Time
		recent bool
	}{
		{"a template", "clowder_tmpl_1000_abcdef01", time.Unix(1000, 0), false},
		{"a copy", "clowder_t_2000_abcdef01", time.Unix(2000, 0), false},
		{"no time in the name", "clowder_t_x_abcdef01", now, true},
		{"another database", "postgres", now, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := databaseCreatedAt(tt.in)
			if tt.recent {
				if d := time.Since(got); d < 0 || d > time.Minute {
					t.Errorf("databaseCreatedAt(%q) = %v, want about now", tt.in, got)
				}
				return
			}
			if !got.Equal(tt.wantAt) {
				t.Errorf("databaseCreatedAt(%q) = %v, want %v", tt.in, got, tt.wantAt)
			}
		})
	}
	// A name that newDatabaseName made is read back as about now, is a valid
	// identifier of letters, digits and underscores, and differs from the next.
	a, b := newDatabaseName(pgCopyPrefix), newDatabaseName(pgCopyPrefix)
	if a == b || !strings.HasPrefix(a, pgCopyPrefix) || len(a) > 63 {
		t.Errorf("names %q and %q", a, b)
	}
	for _, r := range a {
		if r != '_' && (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			t.Errorf("the name %q holds %q", a, r)
		}
	}
	if d := time.Since(databaseCreatedAt(a)); d < 0 || d > time.Minute {
		t.Errorf("databaseCreatedAt(%q) is %v ago", a, d)
	}
	// The template prefix is not the copy prefix, so the sweep patterns match both.
	if strings.HasPrefix(pgTemplatePrefix, pgCopyPrefix) {
		t.Error("the prefix of the templates starts with the prefix of the copies")
	}
}

// TestPostgresTemplateOnAServer shows, on a real server, that copies are
// independent, are dropped by their release function, can be made at the same
// time, and that a new template sweeps the databases that an old run left.
func TestPostgresTemplateOnAServer(t *testing.T) {
	skipUnlessPostgres(t)
	ctx := t.Context()
	tmpl, err := NewPostgresTemplate(ctx, os.Getenv(PostgresTestEnv))
	if err != nil {
		t.Fatalf("NewPostgresTemplate: %+v", err)
	}
	t.Cleanup(func() {
		if err := tmpl.dropDatabase(context.Background(), tmpl.name); err != nil {
			t.Errorf("drop the template: %+v", err)
		}
		if err := tmpl.admin.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	exists := func(name string) bool {
		t.Helper()
		var n int
		if err := tmpl.admin.QueryRowContext(ctx, "SELECT count(*) FROM pg_database WHERE datname = $1", name).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n == 1
	}
	currentDatabase := func(s *Store) string {
		t.Helper()
		var name string
		if err := s.db(ctx).Raw("SELECT current_database()").Scan(&name).Error; err != nil {
			t.Fatal(err)
		}
		return name
	}

	t.Run("copies are independent and dropped on release", func(t *testing.T) {
		a, releaseA, err := tmpl.Open(ctx, clock.NewFake(fakeStart), nil)
		if err != nil {
			t.Fatalf("Open: %+v", err)
		}
		b, releaseB, err := tmpl.Open(ctx, clock.NewFake(fakeStart), nil)
		if err != nil {
			t.Fatalf("Open: %+v", err)
		}
		nameA, nameB := currentDatabase(a), currentDatabase(b)
		if nameA == nameB || !strings.HasPrefix(nameA, pgCopyPrefix) || !exists(nameA) || !exists(nameB) {
			t.Errorf("databases %q and %q", nameA, nameB)
		}
		u := &User{Username: "ann", DisplayName: "Ann", PasswordHash: "h", Role: RoleMember}
		if err := a.CreateUser(ctx, u); err != nil {
			t.Fatalf("CreateUser: %+v", err)
		}
		_ = requireRoute(t, errOf(b.UserByID(ctx, u.ID)), "UserByID.NotFound")
		for _, release := range []func() error{releaseA, releaseB} {
			if err := release(); err != nil {
				t.Errorf("release: %+v", err)
			}
		}
		if exists(nameA) || exists(nameB) {
			t.Errorf("the databases %q and %q still exist after the release", nameA, nameB)
		}
	})

	t.Run("copies made at the same time", func(t *testing.T) {
		var wg sync.WaitGroup
		errs := make(chan error, 8)
		for range 8 {
			wg.Go(func() {
				s, release, err := tmpl.Open(ctx, clock.NewFake(fakeStart), nil)
				if err != nil {
					errs <- err
					return
				}
				if err := s.Tx(ctx, func(*Store) error { return nil }); err != nil {
					errs <- err
				}
				if err := release(); err != nil {
					errs <- err
				}
			})
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Errorf("%+v", err)
		}
	})

	t.Run("a new template drops the old leftovers and keeps the recent ones", func(t *testing.T) {
		old := fmt.Sprintf("%s%d_deadbeef", pgCopyPrefix, time.Now().Add(-2*pgLeftoverAge).Unix())
		oldTemplate := fmt.Sprintf("%s%d_deadbeef", pgTemplatePrefix, time.Now().Add(-2*pgLeftoverAge).Unix())
		recent := fmt.Sprintf("%s%d_deadbeef", pgCopyPrefix, time.Now().Unix())
		for _, name := range []string{old, oldTemplate, recent} {
			if err := tmpl.createDatabase(ctx, name, ""); err != nil {
				t.Fatalf("create %s: %+v", name, err)
			}
		}
		t.Cleanup(func() {
			if err := tmpl.dropDatabase(context.Background(), recent); err != nil {
				t.Errorf("drop: %+v", err)
			}
		})
		second, err := NewPostgresTemplate(ctx, os.Getenv(PostgresTestEnv))
		if err != nil {
			t.Fatalf("NewPostgresTemplate: %+v", err)
		}
		defer func() {
			if err := second.dropDatabase(context.Background(), second.name); err != nil {
				t.Errorf("drop: %+v", err)
			}
			if err := second.admin.Close(); err != nil {
				t.Errorf("close: %v", err)
			}
		}()
		if exists(old) || exists(oldTemplate) {
			t.Errorf("a leftover older than %v survived", pgLeftoverAge)
		}
		if !exists(recent) || !exists(tmpl.name) {
			t.Error("the sweep dropped a recent database, which another run may use")
		}
	})
}

// TestNewPostgresDatabase shows, on a real server, that the database is new
// and empty, that its URL opens it, and that the drop function removes it.
func TestNewPostgresDatabase(t *testing.T) {
	skipUnlessPostgres(t)
	ctx := t.Context()
	dbURL, drop, err := NewPostgresDatabase(ctx, os.Getenv(PostgresTestEnv))
	if err != nil {
		t.Fatalf("NewPostgresDatabase: %+v", err)
	}
	dropped := false
	t.Cleanup(func() {
		if !dropped {
			_ = drop()
		}
	})
	u, err := url.Parse(dbURL)
	if err != nil || !strings.HasPrefix(strings.TrimPrefix(u.Path, "/"), pgCopyPrefix) {
		t.Fatalf("database URL path = %q, %v; want a name with the prefix %q", u.Path, err, pgCopyPrefix)
	}
	s, err := OpenPostgres(ctx, dbURL, clock.NewFake(fakeStart), nil)
	if err != nil {
		t.Fatalf("OpenPostgres on the new database: %+v", err)
	}
	var tables int
	if err := s.db(ctx).Raw("SELECT count(*) FROM information_schema.tables WHERE table_name = 'schema_migrations'").Scan(&tables).Error; err != nil || tables != 1 {
		t.Errorf("schema_migrations tables = %d, %v", tables, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := drop(); err != nil {
		t.Fatalf("drop: %+v", err)
	}
	dropped = true
	if _, err := OpenPostgres(ctx, dbURL, clock.NewFake(fakeStart), nil); err == nil {
		t.Error("the database still opens after the drop")
	}
}
