package auth

import (
	"regexp"
	"testing"
)

func TestNewToken(t *testing.T) {
	alphabet := regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
	seen := make(map[string]bool, 1000)
	for range 1000 {
		tok, err := NewToken()
		if err != nil {
			t.Fatal(err)
		}
		if len(tok) != 43 {
			t.Fatalf("len(%q) = %d, want 43", tok, len(tok))
		}
		if !alphabet.MatchString(tok) {
			t.Fatalf("token %q is not URL-safe base64 without padding", tok)
		}
		if seen[tok] {
			t.Fatalf("duplicate token %q", tok)
		}
		seen[tok] = true
	}
}

func TestHashToken(t *testing.T) {
	tests := []struct {
		name  string
		token string
		want  string
	}{
		{"empty", "", "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"},
		{"abc", "abc", "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := HashToken(tt.token)
			if got != tt.want {
				t.Errorf("HashToken(%q) = %s, want %s", tt.token, got, tt.want)
			}
			if len(got) != 64 {
				t.Errorf("len = %d, want 64", len(got))
			}
		})
	}
}

func TestTokensEqual(t *testing.T) {
	tests := []struct {
		name string
		a, b string
		want bool
	}{
		{"same", "abc", "abc", true},
		{"both empty", "", "", true},
		{"different", "abc", "abd", false},
		{"different length", "abc", "abcd", false},
		{"one empty", "abc", "", false},
		{"case differs", "abc", "ABC", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := TokensEqual(tt.a, tt.b); got != tt.want {
				t.Errorf("TokensEqual(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}
