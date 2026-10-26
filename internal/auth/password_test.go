package auth

import (
	"strings"
	"testing"

	"github.com/rm4n0s/errors"
)

func TestHashVerify(t *testing.T) {
	h := FastHasher()
	other := Hasher{memoryKiB: 16, iterations: 2, parallelism: 2}
	tests := []struct {
		name     string
		hasher   Hasher
		password string
		attempt  string
		want     bool
	}{
		{"right password", h, "correct horse", "correct horse", true},
		{"wrong password", h, "correct horse", "battery staple", false},
		{"different case", h, "Correct", "correct", false},
		{"empty password", h, "", "", true},
		{"empty attempt against a password", h, "secret", "", false},
		{"password against an empty one", h, "", "secret", false},
		{"unicode password", h, "pässwörd-日本語-🐱", "pässwörd-日本語-🐱", true},
		{"unicode password wrong", h, "pässwörd-日本語-🐱", "passwörd-日本語-🐱", false},
		{"hash made with other parameters, right", other, "correct horse", "correct horse", true},
		{"hash made with other parameters, wrong", other, "correct horse", "wrong", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded, err := tt.hasher.Hash(tt.password)
			if err != nil {
				t.Fatalf("Hash: %v", err)
			}
			// Verify is called on a Hasher with other parameters than the one that hashed.
			got, err := h.Verify(tt.attempt, encoded)
			if err != nil {
				t.Fatalf("Verify: %v", err)
			}
			if got != tt.want {
				t.Errorf("Verify = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHashFormat(t *testing.T) {
	encoded, err := FastHasher().Hash("x")
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" || parts[2] != "v=19" || parts[3] != "m=8,t=1,p=1" {
		t.Errorf("unexpected PHC string %q", encoded)
	}
	// 16 bytes of salt and 32 bytes of key in unpadded standard base64.
	if len(parts[4]) != 22 || len(parts[5]) != 43 {
		t.Errorf("salt/key lengths = %d/%d, want 22/43", len(parts[4]), len(parts[5]))
	}
}

func TestHashSalted(t *testing.T) {
	h := FastHasher()
	a, err := h.Hash("same")
	if err != nil {
		t.Fatal(err)
	}
	b, err := h.Hash("same")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Errorf("two hashes of the same password are equal: %q", a)
	}
}

func TestDefaultHasherParameters(t *testing.T) {
	h := DefaultHasher()
	encoded, err := h.Hash("default parameters")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(encoded, "$argon2id$v=19$m=65536,t=3,p=2$") {
		t.Errorf("unexpected PHC string %q", encoded)
	}
	ok, err := h.Verify("default parameters", encoded)
	if err != nil || !ok {
		t.Errorf("Verify = %v, %v, want true, nil", ok, err)
	}
}

func TestVerifyMalformed(t *testing.T) {
	const (
		salt = "c29tZXNhbHRzb21lc2FsdA" // 16 bytes
		key  = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	)
	tests := []struct {
		name    string
		encoded string
		route   string
	}{
		{"empty", "", "Hasher.Verify->parsePHC.PasswordHashMalformed"},
		{"plain text", "not a hash", "Hasher.Verify->parsePHC.PasswordHashMalformed"},
		{"too few parts", "$argon2id$v=19$m=8,t=1,p=1$" + salt, "Hasher.Verify->parsePHC.PasswordHashMalformed"},
		{"too many parts", "$argon2id$v=19$m=8,t=1,p=1$" + salt + "$" + key + "$extra", "Hasher.Verify->parsePHC.PasswordHashMalformed"},
		{"no leading dollar", "x$argon2id$v=19$m=8,t=1,p=1$" + salt + "$" + key, "Hasher.Verify->parsePHC.PasswordHashMalformed"},
		{"other algorithm", "$argon2i$v=19$m=8,t=1,p=1$" + salt + "$" + key, "Hasher.Verify->parsePHC.PasswordHashUnsupported"},
		{"bcrypt", "$2a$10$abcdefghijklmnopqrstuuabcdefghijklmnopqrstuvwxyz01234", "Hasher.Verify->parsePHC.PasswordHashMalformed"},
		{"other version", "$argon2id$v=16$m=8,t=1,p=1$" + salt + "$" + key, "Hasher.Verify->parsePHC.PasswordHashUnsupported"},
		{"version without v=", "$argon2id$19$m=8,t=1,p=1$" + salt + "$" + key, "Hasher.Verify->parsePHC.PasswordHashMalformed"},
		{"version not a number", "$argon2id$v=x$m=8,t=1,p=1$" + salt + "$" + key, "Hasher.Verify->parsePHC.PasswordHashMalformed"},
		{"params with two items", "$argon2id$v=19$m=8,t=1$" + salt + "$" + key, "Hasher.Verify->parsePHC.PasswordHashMalformed"},
		{"params in the wrong order", "$argon2id$v=19$t=1,m=8,p=1$" + salt + "$" + key, "Hasher.Verify->parsePHC.PasswordHashMalformed"},
		{"param not a number", "$argon2id$v=19$m=big,t=1,p=1$" + salt + "$" + key, "Hasher.Verify->parsePHC.PasswordHashMalformed"},
		{"negative param", "$argon2id$v=19$m=-8,t=1,p=1$" + salt + "$" + key, "Hasher.Verify->parsePHC.PasswordHashMalformed"},
		{"absurd memory", "$argon2id$v=19$m=4294967295,t=1,p=1$" + salt + "$" + key, "Hasher.Verify->parsePHC.PasswordHashMalformed"},
		{"memory just above 1 GiB", "$argon2id$v=19$m=1048577,t=1,p=1$" + salt + "$" + key, "Hasher.Verify->parsePHC.PasswordHashMalformed"},
		{"zero memory", "$argon2id$v=19$m=0,t=1,p=1$" + salt + "$" + key, "Hasher.Verify->parsePHC.PasswordHashMalformed"},
		{"zero time", "$argon2id$v=19$m=8,t=0,p=1$" + salt + "$" + key, "Hasher.Verify->parsePHC.PasswordHashMalformed"},
		{"absurd time", "$argon2id$v=19$m=8,t=4294967295,p=1$" + salt + "$" + key, "Hasher.Verify->parsePHC.PasswordHashMalformed"},
		{"zero threads", "$argon2id$v=19$m=8,t=1,p=0$" + salt + "$" + key, "Hasher.Verify->parsePHC.PasswordHashMalformed"},
		{"too many threads", "$argon2id$v=19$m=8,t=1,p=256$" + salt + "$" + key, "Hasher.Verify->parsePHC.PasswordHashMalformed"},
		{"memory too small for threads", "$argon2id$v=19$m=8,t=1,p=2$" + salt + "$" + key, "Hasher.Verify->parsePHC.PasswordHashMalformed"},
		{"bad base64 salt", "$argon2id$v=19$m=8,t=1,p=1$!!!$" + key, "Hasher.Verify->parsePHC.PasswordHashMalformed"},
		{"bad base64 key", "$argon2id$v=19$m=8,t=1,p=1$" + salt + "$!!!", "Hasher.Verify->parsePHC.PasswordHashMalformed"},
		{"padded base64 key", "$argon2id$v=19$m=8,t=1,p=1$" + salt + "$AAAA====", "Hasher.Verify->parsePHC.PasswordHashMalformed"},
		{"empty salt", "$argon2id$v=19$m=8,t=1,p=1$$" + key, "Hasher.Verify->parsePHC.PasswordHashMalformed"},
		{"empty key", "$argon2id$v=19$m=8,t=1,p=1$" + salt + "$", "Hasher.Verify->parsePHC.PasswordHashMalformed"},
	}
	h := FastHasher()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ok, err := h.Verify("password", tt.encoded)
			if ok {
				t.Error("Verify = true, want false")
			}
			appErr, isErr := errors.FromError(err)
			if !isErr {
				t.Fatalf("expected *errors.Error, got %T: %v", err, err)
			}
			if !appErr.HasRoute(tt.route) {
				t.Errorf("unexpected failure path: %s, want %s", appErr.Route(), tt.route)
			}
		})
	}
}

func TestVerifyTags(t *testing.T) {
	h := FastHasher()
	_, err := h.Verify("p", "garbage")
	if !errors.Is(err, &errors.Error{Tag: TagPasswordHashMalformed}) {
		t.Errorf("err = %v, want tag %s", err, TagPasswordHashMalformed)
	}
	_, err = h.Verify("p", "$argon2i$v=19$m=8,t=1,p=1$c29tZXNhbHRzb21lc2FsdA$AAAA")
	if !errors.Is(err, &errors.Error{Tag: TagPasswordHashUnsupported}) {
		t.Errorf("err = %v, want tag %s", err, TagPasswordHashUnsupported)
	}
}
