package store

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rm4n0s/clowder/internal/clock"
)

func TestLatestMigration(t *testing.T) {
	list := migrationList()
	if got, want := LatestMigration(), list[len(list)-1].ID; got != want || got == "" {
		t.Errorf("LatestMigration = %q, want %q", got, want)
	}
	// The IDs sort in the order of the list: a restore compares them as text.
	for i := 1; i < len(list); i++ {
		if list[i-1].ID >= list[i].ID {
			t.Errorf("migration %q does not sort after %q", list[i].ID, list[i-1].ID)
		}
	}
}

func TestSchemaVersion(t *testing.T) {
	f := newFix(t)
	got, err := f.s.SchemaVersion(f.ctx)
	if err != nil || got != LatestMigration() {
		t.Errorf("SchemaVersion = %q, %v; want %q", got, err, LatestMigration())
	}
	if err := f.s.db(f.ctx).Exec("DELETE FROM schema_migrations").Error; err != nil {
		t.Fatal(err)
	}
	if got, err := f.s.SchemaVersion(f.ctx); err != nil || got != "" {
		t.Errorf("SchemaVersion of a database with no migration = %q, %v; want empty", got, err)
	}
	if err := f.s.db(f.ctx).Exec("DROP TABLE schema_migrations").Error; err != nil {
		t.Fatal(err)
	}
	_, err = f.s.SchemaVersion(f.ctx)
	_ = requireRoute(t, err, "Store.SchemaVersion.QueryFailed")
}

func TestBackupCounts(t *testing.T) {
	f := newFix(t)
	got, err := f.s.BackupCounts(f.ctx)
	if err != nil || got != (BackupCounts{}) {
		t.Fatalf("BackupCounts of an empty database = %+v, %v", got, err)
	}
	w := newStatsWorld(t)
	got, err = w.f.s.BackupCounts(w.f.ctx)
	if err != nil {
		t.Fatalf("BackupCounts: %+v", err)
	}
	if want := (BackupCounts{Users: 2, Rooms: 2, Posts: 2}); got != want {
		t.Errorf("BackupCounts = %+v, want %+v", got, want)
	}
	drop := "DROP TABLE messages"
	if onPostgres() {
		drop += " CASCADE"
	}
	if err := w.f.s.db(w.f.ctx).Exec(drop).Error; err != nil {
		t.Fatal(err)
	}
	_, err = w.f.s.BackupCounts(w.f.ctx)
	_ = requireRoute(t, err, "Store.BackupCounts.QueryFailed")
}

func TestIsPostgres(t *testing.T) {
	f := newFix(t)
	if got := f.s.IsPostgres(); got != onPostgres() {
		t.Errorf("IsPostgres = %v, want %v", got, onPostgres())
	}
}

func TestVacuumInto(t *testing.T) {
	if onPostgres() {
		f := newFix(t)
		err := f.s.VacuumInto(f.ctx, filepath.Join(t.TempDir(), "x.db"))
		appErr := requireRoute(t, err, "Store.VacuumInto.BackupVacuumFailed")
		if appErr.Tag != TagBackupVacuumFailed {
			t.Errorf("tag = %s", appErr.Tag)
		}
		return
	}

	t.Run("a copy that opens and has the rows", func(t *testing.T) {
		w := newStatsWorld(t)
		dir := t.TempDir()
		// A name with a quote, and an empty file already there, as backup makes it.
		dest := filepath.Join(dir, "it's a copy.db")
		if err := os.WriteFile(dest, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := w.f.s.VacuumInto(w.f.ctx, dest); err != nil {
			t.Fatalf("VacuumInto: %+v", err)
		}
		cp, err := open(w.f.ctx, dest, clock.NewFake(fakeStart), nil, defaultOptions())
		if err != nil {
			t.Fatalf("open the copy: %+v", err)
		}
		defer func() { _ = cp.Close() }()
		want, err := w.f.s.Stats(w.f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if got, err := cp.Stats(w.f.ctx); err != nil || got != want {
			t.Errorf("Stats of the copy = %+v, %v; want %+v", got, err, want)
		}
		var check string
		if err := cp.db(w.f.ctx).Raw("PRAGMA integrity_check").Scan(&check).Error; err != nil || check != "ok" {
			t.Errorf("integrity_check = %q, %v", check, err)
		}
	})

	t.Run("a path that cannot be written", func(t *testing.T) {
		f := newFix(t)
		err := f.s.VacuumInto(f.ctx, filepath.Join(t.TempDir(), "no", "such", "dir", "x.db"))
		appErr := requireRoute(t, err, "Store.VacuumInto.BackupVacuumFailed")
		if appErr.Metadata["path"] == "" {
			t.Errorf("metadata = %v", appErr.Metadata)
		}
	})

	t.Run("a file that is not empty", func(t *testing.T) {
		f := newFix(t)
		dest := filepath.Join(t.TempDir(), "x.db")
		if err := os.WriteFile(dest, []byte("something"), 0o600); err != nil {
			t.Fatal(err)
		}
		_ = requireRoute(t, f.s.VacuumInto(f.ctx, dest), "Store.VacuumInto.BackupVacuumFailed")
		if b, err := os.ReadFile(dest); err != nil || string(b) != "something" {
			t.Errorf("the file was changed: %q, %v", b, err)
		}
	})
}
