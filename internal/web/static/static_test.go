package static_test

import (
	"testing"

	"github.com/rm4n0s/clowder/internal/web/static"
)

func TestFS(t *testing.T) {
	tests := []struct {
		name string
	}{
		{"datastar.js"},
		{"app.css"},
		{"board.js"},
		{"messages.js"},
		{"notify.js"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := static.FS.ReadFile(tt.name)
			if err != nil {
				t.Fatalf("read %s: %v", tt.name, err)
			}
			if len(data) == 0 {
				t.Errorf("%s is empty", tt.name)
			}
		})
	}
}
