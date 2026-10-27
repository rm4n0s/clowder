package store

import (
	"runtime"
	"testing"
)

func TestPoolSize(t *testing.T) {
	tests := []struct {
		cpus, want int
	}{
		{1, minOpenConns},
		{4, minOpenConns},
		{8, 16},
		{16, maxOpenConns},
		{64, maxOpenConns},
	}
	for _, tt := range tests {
		if got := poolSize(tt.cpus); got != tt.want {
			t.Errorf("poolSize(%d) = %d, want %d", tt.cpus, got, tt.want)
		}
	}
}

func TestOpenBoundsThePoolOfAFileDatabase(t *testing.T) {
	f := newFix(t)
	sqlDB, err := f.s.gdb.DB()
	if err != nil {
		t.Fatalf("DB: %v", err)
	}
	if got, want := sqlDB.Stats().MaxOpenConnections, poolSize(runtime.NumCPU()); got != want {
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
