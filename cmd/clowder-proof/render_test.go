//go:build clowder_browser

package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rm4n0s/errors"
)

// pngInfo reads the size and the color type of a PNG from its IHDR.
func pngInfo(t *testing.T, data []byte) (width, height uint32, colorType byte) {
	t.Helper()
	if len(data) < 26 || !bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")) || string(data[12:16]) != "IHDR" {
		t.Fatalf("not a PNG (%d bytes)", len(data))
	}
	return binary.BigEndian.Uint32(data[16:20]), binary.BigEndian.Uint32(data[20:24]), data[25]
}

// TestRenderIcon renders a small SVG: the PNG has the size asked for, a
// transparent ground (color type 6, RGBA) unless -icon-bg is given (2, RGB).
func TestRenderIcon(t *testing.T) {
	dir := t.TempDir()
	svg := filepath.Join(dir, "dot.svg")
	if err := os.WriteFile(svg, []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><circle cx="5" cy="5" r="4" fill="#c2410c"/></svg>`), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name      string
		size      int
		bg        string
		pad       int
		wantColor byte
	}{
		{"transparent", 32, "", 0, 6},
		{"large with a ground and padding", 180, "#fbf6ee", 20, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := filepath.Join(dir, tt.name+".png")
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			var stdout bytes.Buffer
			o := options{renderIcon: svg, out: out, iconSize: tt.size, iconBackground: tt.bg, iconPadding: tt.pad, wait: 0}
			if err := renderIcon(ctx, o, &stdout); err != nil {
				t.Fatalf("renderIcon: %+v", err)
			}
			data, err := os.ReadFile(out)
			if err != nil {
				t.Fatal(err)
			}
			w, h, color := pngInfo(t, data)
			if int(w) != tt.size || int(h) != tt.size || color != tt.wantColor {
				t.Errorf("PNG = %dx%d color type %d, want %dx%d color type %d", w, h, color, tt.size, tt.size, tt.wantColor)
			}
			if stdout.Len() == 0 {
				t.Error("nothing was said on stdout")
			}
		})
	}
}

func TestRenderIconErrors(t *testing.T) {
	dir := t.TempDir()
	notSVG := filepath.Join(dir, "x.svg")
	if err := os.WriteFile(notSVG, []byte("not an svg"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		svg  string
	}{
		{"missing file", filepath.Join(dir, "missing.svg")},
		{"not an svg", notSVG},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			o := options{renderIcon: tt.svg, out: filepath.Join(dir, "out.png"), iconSize: 32}
			err := renderIcon(ctx, o, &bytes.Buffer{})
			appErr, ok := errors.FromError(err)
			if !ok {
				t.Fatalf("expected *errors.Error, got %T: %v", err, err)
			}
			if !appErr.HasRoute("renderIcon.IconRenderFailed") {
				t.Errorf("unexpected failure path: %s", appErr.Route())
			}
		})
	}
}
