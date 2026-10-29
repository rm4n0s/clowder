package store

import (
	"runtime"
	"testing"
)

func TestPoolSize(t *testing.T) {
	tests := []struct {
		cpus int
		d    dialect
		want int
	}{
		{1, dialectSQLite, minOpenConns},
		{4, dialectSQLite, minOpenConns},
		{6, dialectSQLite, 12},
		{8, dialectSQLite, maxSQLiteOpenConns},
		{64, dialectSQLite, maxSQLiteOpenConns},
		{1, dialectPostgres, minOpenConns},
		{4, dialectPostgres, minOpenConns},
		{8, dialectPostgres, 16},
		{16, dialectPostgres, maxOpenConns},
		{64, dialectPostgres, maxOpenConns},
	}
	for _, tt := range tests {
		if got := poolSize(tt.cpus, tt.d); got != tt.want {
			t.Errorf("poolSize(%d, %s) = %d, want %d", tt.cpus, tt.d, got, tt.want)
		}
	}
}

func TestOpenBoundsThePoolOfAFileDatabase(t *testing.T) {
	f := newFix(t)
	sqlDB, err := f.s.gdb.DB()
	if err != nil {
		t.Fatalf("DB: %v", err)
	}
	if got, want := sqlDB.Stats().MaxOpenConnections, poolSize(runtime.NumCPU(), f.s.dialect); got != want {
		t.Errorf("MaxOpenConnections = %d, want %d", got, want)
	}
	// A burst of queries uses no more connections than the pool allows and
	// leaves them open for the next burst.
	errs := make(chan error, 3*maxOpenConns)
	for range cap(errs) {
		go func() {
			var n int64
			errs <- f.s.db(f.ctx).Raw("SELECT count(*) FROM users").Scan(&n).Error
		}()
	}
	for range cap(errs) {
		if err := <-errs; err != nil {
			t.Errorf("query: %v", err)
		}
	}
	if st := sqlDB.Stats(); st.OpenConnections > st.MaxOpenConnections || st.Idle != st.OpenConnections {
		t.Errorf("after the burst: open %d, idle %d, max %d", st.OpenConnections, st.Idle, st.MaxOpenConnections)
	}
}
