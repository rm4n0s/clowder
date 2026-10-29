package app_test

import (
	"crypto/elliptic"
	"strings"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
	"golang.org/x/crypto/ssh"
)

// FuzzParseAuthorizedKey throws text at the parsing of the public key of the
// settings form (step 24.1): the line is parsed with parseKeyLine, which
// Add calls, and what comes out is looked at the way Add looks at it. It must
// never panic or take long, and a key it returns must have a type, a
// fingerprint, a size, a normal form that parses to the same key, and an RSA
// size that is read without a failure. The seeds are valid keys of every type
// of SPEC §7.16, keys that must be refused, and broken lines; more are in
// testdata/fuzz/FuzzParseAuthorizedKey.
func FuzzParseAuthorizedKey(f *testing.F) {
	ed := newEd25519Line(f)
	seeds := []string{
		ed,
		ecdsaLine(f, elliptic.P256()),
		ecdsaLine(f, elliptic.P384()),
		ecdsaLine(f, elliptic.P521()),
		lineOf(f, rsaKeys()[3072].Public(), "rsa"),
		lineOf(f, rsaKeys()[2048].Public(), "short"),
		dsaLine, skEd25519Line, skECDSALine,
		"",
		" \t ",
		"ssh-ed25519",
		"ssh-ed25519 ",
		"ssh-ed25519 AAAA",
		"ssh-ed25519 !!!!",
		"ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAAAgQ",
		`command="rm -rf /" ` + ed,
		`no-pty,from="10.0.0.1" ` + ed,
		ed + "\n" + ed,
		"# a comment\n" + ed,
		"garbage\n" + ed,
		strings.Repeat("a", 100_000),
		strings.Repeat("ssh-ed25519 ", 5000),
		"ssh-ed25519 " + strings.Repeat("A", 70_000),
		"cert-authority " + ed,
		"ssh-ed25519-cert-v01@openssh.com AAAAIHNzaC1lZDI1NTE5LWNlcnQtdjAxQG9wZW5zc2guY29t",
		"\x00ssh-ed25519 AAAA\x00",
		"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIH5pvqGW/eu0bmPl9E6Wlx6daTyICiTW/Hd/06O8Un3X comment \u202e with unicode",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, line string) {
		start := time.Now()
		pub, onlyOne := app.ParseKeyLine(line)
		if took := time.Since(start); took > 2*time.Second {
			t.Fatalf("parsing took %v", took)
		}
		if pub == nil {
			if onlyOne {
				t.Fatalf("no key, and onlyOne is true")
			}
			return
		}
		if pub.Type() == "" {
			t.Fatalf("a key with no type")
		}
		fp := ssh.FingerprintSHA256(pub)
		if !strings.HasPrefix(fp, "SHA256:") {
			t.Fatalf("fingerprint %q", fp)
		}
		if app.AcceptedKeyType(pub.Type()) && pub.Type() == ssh.KeyAlgoRSA && app.RSABits(pub) == 0 {
			t.Fatalf("an RSA key of no size")
		}
		// What Add stores is the normal form: it parses again, as one key
		// with nothing around it, to the same fingerprint.
		normal := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub)))
		again, onlyAgain := app.ParseKeyLine(normal)
		if again == nil || !onlyAgain || ssh.FingerprintSHA256(again) != fp {
			t.Fatalf("the normal form %q does not parse back to the key (%v, %v)", normal, again, onlyAgain)
		}
		if strings.ContainsAny(normal, "\n\r") {
			t.Fatalf("the normal form holds a line break: %q", normal)
		}
	})
}
