package views_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/rm4n0s/clowder/internal/web/views"
)

func TestErrorPage(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		message string
		want    []string
		notWant []string
	}{
		{
			name:    "not found",
			status:  404,
			message: "Page not found",
			want:    []string{"<title>404 Not Found · Clowder</title>", "<h1>404</h1>", "<p>Page not found</p>", "<main>"},
		},
		{
			name:    "internal server error",
			status:  500,
			message: "Something went wrong",
			want:    []string{"<title>500 Internal Server Error · Clowder</title>", "<h1>500</h1>", "<p>Something went wrong</p>"},
		},
		{
			name:    "message is escaped",
			status:  400,
			message: "<script>alert(1)</script>",
			want:    []string{"&lt;script&gt;alert(1)&lt;/script&gt;"},
			notWant: []string{"<script>alert(1)</script>"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := views.ErrorPage(tt.status, tt.message).Render(context.Background(), &buf); err != nil {
				t.Fatalf("render ErrorPage: %v", err)
			}
			html := buf.String()
			for _, w := range tt.want {
				if !strings.Contains(html, w) {
					t.Errorf("page does not contain %q:\n%s", w, html)
				}
			}
			for _, w := range tt.notWant {
				if strings.Contains(html, w) {
					t.Errorf("page contains %q:\n%s", w, html)
				}
			}
		})
	}
}
