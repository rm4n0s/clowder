//go:build clowder_browser

package main

import (
	"bytes"
	"testing"
	"time"

	"github.com/rm4n0s/errors"
)

func TestParseFlags(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    options
		wantTag string
	}{
		{
			name: "defaults",
			args: []string{"-o", "x.png"},
			want: options{page: "/", out: "x.png", width: 1280, height: 800, wait: 500 * time.Millisecond},
		},
		{
			name: "all flags",
			args: []string{"-o", "x.png", "-page", "/rooms/1", "-as", "ana", "-width", "400", "-height", "300", "-wait", "1s"},
			want: options{page: "/rooms/1", out: "x.png", as: "ana", width: 400, height: 300, wait: time.Second},
		},
		{name: "output missing", args: nil, wantTag: tagFlagsInvalid},
		{name: "page without slash", args: []string{"-o", "x.png", "-page", "rooms"}, wantTag: tagFlagsInvalid},
		{name: "zero width", args: []string{"-o", "x.png", "-width", "0"}, wantTag: tagFlagsInvalid},
		{name: "negative wait", args: []string{"-o", "x.png", "-wait", "-1s"}, wantTag: tagFlagsInvalid},
		{name: "unknown flag", args: []string{"-nope"}, wantTag: tagFlagsInvalid},
		{name: "extra argument", args: []string{"-o", "x.png", "more"}, wantTag: tagFlagsInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stderr bytes.Buffer
			got, err := parseFlags(tt.args, &stderr)
			if tt.wantTag == "" {
				if err != nil {
					t.Fatalf("parseFlags: %v", err)
				}
				if got != tt.want {
					t.Errorf("options = %+v, want %+v", got, tt.want)
				}
				return
			}
			appErr, ok := errors.FromError(err)
			if !ok {
				t.Fatalf("expected *errors.Error, got %T: %v", err, err)
			}
			if appErr.Tag != tt.wantTag || !appErr.HasRoute("parseFlags.FlagsInvalid") {
				t.Errorf("tag = %q, route = %s", appErr.Tag, appErr.Route())
			}
		})
	}
}
