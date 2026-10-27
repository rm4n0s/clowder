package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"strconv"
	"strings"

	"github.com/rm4n0s/errors"
	"golang.org/x/crypto/argon2"
)

// The parameters of DefaultHasher and the sizes every Hasher uses.
const (
	defaultMemoryKiB   = 64 * 1024 // 64 MiB
	defaultIterations  = 3
	defaultParallelism = 2
	saltLength         = 16
	keyLength          = 32
)

// The limits a stored hash must respect. A corrupt or hostile row must not be
// able to make Verify exhaust memory or run for minutes.
const (
	maxMemoryKiB   = 1024 * 1024 // 1 GiB
	maxIterations  = 100
	maxParallelism = 64
	maxSaltLength  = 1024
	maxKeyLength   = 1024
)

// phcParts is the number of "$"-separated parts of an argon2id PHC string,
// counting the empty part before the first "$".
const phcParts = 6

// Hasher hashes and verifies passwords with argon2id. The zero value is not
// usable: make one with DefaultHasher (or FastHasher in tests). A Hasher holds
// no state besides its parameters, so it is safe for concurrent use.
type Hasher struct {
	memoryKiB   uint32
	iterations  uint32
	parallelism uint8
}

// DefaultHasher returns the Hasher of the running program: argon2id with 64 MiB
// of memory, 3 iterations and 2 threads.
func DefaultHasher() Hasher {
	return Hasher{
		memoryKiB:   defaultMemoryKiB,
		iterations:  defaultIterations,
		parallelism: defaultParallelism,
	}
}

// MemoryBytes is the memory one hash or one check of a password takes, which
// the Go runtime keeps as part of the process after the call: 64 MiB for
// DefaultHasher.
func (h Hasher) MemoryBytes() uint64 {
	return uint64(h.memoryKiB) * 1024
}

// FastHasher returns a Hasher with tiny parameters so that tests run quickly.
// It is for tests only: its hashes are not safe to keep.
func FastHasher() Hasher {
	return Hasher{memoryKiB: 8, iterations: 1, parallelism: 1}
}

// Hash returns the argon2id hash of password in the PHC string format:
// $argon2id$v=19$m=65536,t=3,p=2$<salt>$<key>, with the salt and the key in
// standard base64 without padding. The salt is random, so hashing the same
// password twice gives two different strings.
func (h Hasher) Hash(password string) (string, error) {
	salt := make([]byte, saltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", errors.NewErr(tagSaltGenerationFailed, err)
	}
	key := argon2.IDKey([]byte(password), salt, h.iterations, h.memoryKiB, h.parallelism, keyLength)
	var sb strings.Builder
	sb.WriteString("$argon2id$v=")
	sb.WriteString(strconv.Itoa(argon2.Version))
	sb.WriteString("$m=")
	sb.WriteString(strconv.FormatUint(uint64(h.memoryKiB), 10))
	sb.WriteString(",t=")
	sb.WriteString(strconv.FormatUint(uint64(h.iterations), 10))
	sb.WriteString(",p=")
	sb.WriteString(strconv.FormatUint(uint64(h.parallelism), 10))
	sb.WriteString("$")
	sb.WriteString(base64.RawStdEncoding.EncodeToString(salt))
	sb.WriteString("$")
	sb.WriteString(base64.RawStdEncoding.EncodeToString(key))
	return sb.String(), nil
}

// Verify reports whether password matches encoded, a string made by Hash. It
// uses the parameters stored in encoded, not those of h, so hashes made with
// older parameters keep working. A wrong password is false and a nil error. A
// string that is not a valid hash is a PasswordHashMalformed error, and one
// made by another algorithm or version is a PasswordHashUnsupported error.
func (h Hasher) Verify(password, encoded string) (bool, error) {
	p, err := parsePHC(encoded)
	if err != nil {
		return false, err
	}
	got := argon2.IDKey([]byte(password), p.salt, p.iterations, p.memoryKiB, p.parallelism, uint32(len(p.key)))
	return subtle.ConstantTimeCompare(got, p.key) == 1, nil
}

// phcHash is a parsed argon2id PHC string.
type phcHash struct {
	memoryKiB   uint32
	iterations  uint32
	parallelism uint8
	salt        []byte
	key         []byte
}

// parsePHC parses an argon2id PHC string and checks that its parameters are
// within the limits above.
func parsePHC(encoded string) (phcHash, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != phcParts || parts[0] != "" {
		return phcHash{}, errors.New(TagPasswordHashMalformed, "the password hash does not have the PHC format",
			"reason", "parts", "parts", len(parts))
	}
	if parts[1] != "argon2id" {
		return phcHash{}, errors.New(TagPasswordHashUnsupported, "the password hash algorithm is not argon2id",
			"algorithm", parts[1])
	}
	versionText, ok := strings.CutPrefix(parts[2], "v=")
	if !ok {
		return phcHash{}, errors.New(TagPasswordHashMalformed, "the password hash version is malformed",
			"reason", "version")
	}
	version, err := strconv.Atoi(versionText)
	if err != nil {
		return phcHash{}, errors.NewErr(TagPasswordHashMalformed, err).SetMetadata("reason", "version")
	}
	if version != argon2.Version {
		return phcHash{}, errors.New(TagPasswordHashUnsupported, "the password hash version is not supported",
			"version", version)
	}

	var p phcHash
	params := strings.Split(parts[3], ",")
	if len(params) != 3 {
		return phcHash{}, errors.New(TagPasswordHashMalformed, "the password hash parameters are malformed",
			"reason", "params")
	}
	var values [3]uint64
	for i, prefix := range [3]string{"m=", "t=", "p="} {
		text, ok := strings.CutPrefix(params[i], prefix)
		if !ok {
			return phcHash{}, errors.New(TagPasswordHashMalformed, "the password hash parameters are malformed",
				"reason", "params")
		}
		values[i], err = strconv.ParseUint(text, 10, 32)
		if err != nil {
			return phcHash{}, errors.NewErr(TagPasswordHashMalformed, err).SetMetadata("reason", "params")
		}
	}
	memory, iterations, parallelism := values[0], values[1], values[2]
	if memory == 0 || memory > maxMemoryKiB ||
		iterations == 0 || iterations > maxIterations ||
		parallelism == 0 || parallelism > maxParallelism ||
		memory < 8*parallelism {
		return phcHash{}, errors.New(TagPasswordHashMalformed, "the password hash parameters are out of range",
			"reason", "range", "memoryKiB", memory, "iterations", iterations, "parallelism", parallelism)
	}
	p.memoryKiB = uint32(memory)
	p.iterations = uint32(iterations)
	p.parallelism = uint8(parallelism)

	p.salt, err = base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return phcHash{}, errors.NewErr(TagPasswordHashMalformed, err).SetMetadata("reason", "salt")
	}
	p.key, err = base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return phcHash{}, errors.NewErr(TagPasswordHashMalformed, err).SetMetadata("reason", "key")
	}
	if len(p.salt) == 0 || len(p.salt) > maxSaltLength || len(p.key) == 0 || len(p.key) > maxKeyLength {
		return phcHash{}, errors.New(TagPasswordHashMalformed, "the password hash salt or key has a bad length",
			"reason", "length", "saltLength", len(p.salt), "keyLength", len(p.key))
	}
	return p, nil
}

const (
	// TagPasswordHashMalformed marks a stored password hash that is not a valid
	// argon2id PHC string or whose parameters are out of range.
	TagPasswordHashMalformed = "PasswordHashMalformed"
	// TagPasswordHashUnsupported marks a stored password hash made by an
	// algorithm or a version that Verify does not support.
	TagPasswordHashUnsupported = "PasswordHashUnsupported"
	// tagSaltGenerationFailed marks a failure of the random source while
	// making a salt.
	tagSaltGenerationFailed = "SaltGenerationFailed"
)
