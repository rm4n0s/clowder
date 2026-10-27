package app_test

import (
	"context"
	"testing"

	"github.com/rm4n0s/clowder/internal/app"
)

func TestHealthCheck(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		closeDB   bool
		cancelled bool
		wantRoute string
	}{
		{name: "working database"},
		{name: "closed database", closeDB: true, wantRoute: "Health.Check->Store.Ping.StorePingFailed"},
		{name: "cancelled context", cancelled: true, wantRoute: "Health.Check->Store.Ping.StorePingFailed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			h := app.NewHealth(e.st)
			if tt.closeDB {
				if err := e.st.Close(); err != nil {
					t.Fatalf("Close: %v", err)
				}
			}
			ctx := t.Context()
			if tt.cancelled {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			err := h.Check(ctx)
			if tt.wantRoute == "" {
				if err != nil {
					t.Fatalf("Check: %+v", err)
				}
				return
			}
			_ = requireTag(t, err, "StorePingFailed", tt.wantRoute)
		})
	}
}
