package files

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"image"
	_ "image/gif"  // registers the GIF decoder for image.DecodeConfig
	_ "image/jpeg" // registers the JPEG decoder
	_ "image/png"  // registers the PNG decoder
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/rm4n0s/errors"
)

// The permissions of the directories that New makes.
const dirMode = 0o750

const (
	// tmpDirName is the directory of the files being written, inside the
	// files directory. A file is renamed out of it when it is complete, so
	// that a key in the files directory is always a whole file.
	tmpDirName = "tmp"
	// staleTmpAge is how long a file or directory in the tmp directory must
	// have gone untouched before New takes it for the leftover of a process that
	// died. Several processes share the directory (SPEC §8.1), and one that
	// starts must not remove what another is writing: an upload is refused
	// when it takes longer than 15 minutes to arrive (SPEC D37), and every
	// chunk written moves the modification time.
	staleTmpAge = time.Hour
	// keyBytes is the number of random bytes in a key.
	keyBytes = 32
	// keyLength is the length of a key: keyBytes in hex.
	keyLength = keyBytes * 2
	// sniffLength is how many bytes http.DetectContentType looks at.
	sniffLength = 512
	// copyBufferSize is the size of the chunks that Save reads.
	copyBufferSize = 32 << 10
	// unknownType is the content type of a file whose type is not known.
	unknownType = "application/octet-stream"
)

// tempFile is the part of *os.File that Save uses on the file it is writing.
// It is an interface only so that the tests can make each step fail.
type tempFile interface {
	io.Writer
	io.ReaderAt
	Sync() error
	Close() error
	Name() string
}

// Store keeps files on disk under one directory, each under a random key.
// Make it with New. It is safe for concurrent use.
type Store struct {
	dir string
	tmp string
	// random is where keys come from: crypto/rand.Reader, except in tests.
	random io.Reader
	// create makes the temporary file of a Save: os.CreateTemp, except in
	// tests.
	create func(dir, pattern string) (tempFile, error)
}

// Saved is what Save knows about a file it stored.
type Saved struct {
	// Key is the name of the file in the store: 64 lowercase hex characters.
	Key string
	// Size is the size in bytes.
	Size int64
	// ContentType is the type sniffed from the first 512 bytes of the file
	// (http.DetectContentType). For an image (PNG, JPEG, GIF or WebP) whose
	// header cannot be read it is "application/octet-stream": a file that
	// claims to be an image and is not is stored as an opaque file.
	ContentType string
	// Width and Height are the size in pixels of a PNG, JPEG, GIF or WebP
	// image; 0 for any other file.
	Width, Height int
}

// New makes the store in the directory dir, and dir/tmp, creating them with
// the permissions 0750 when they are missing. It removes what an earlier run
// left in dir/tmp, files that were being written when the process died: those
// not modified for an hour (staleTmpAge), because another process that shares
// the directory may be writing one just now. It fails with TagDirCreateFailed when a directory cannot be made and with
// TagTmpCleanFailed when dir/tmp cannot be emptied.
func New(dir string) (*Store, error) {
	tmp := filepath.Join(dir, tmpDirName)
	if err := os.MkdirAll(tmp, dirMode); err != nil {
		return nil, errors.NewErr(tagDirCreateFailed, err).SetMetadata("dir", dir)
	}
	entries, err := os.ReadDir(tmp)
	if err != nil {
		return nil, errors.NewErr(tagTmpCleanFailed, err).SetMetadata("dir", tmp)
	}
	cutoff := time.Now().Add(-staleTmpAge)
	for _, e := range entries {
		info, err := e.Info()
		if errors.Is(err, fs.ErrNotExist) {
			continue // another process removed it
		}
		if err != nil {
			return nil, errors.NewErr(tagTmpCleanFailed, err).SetMetadata("dir", tmp, "name", e.Name())
		}
		if info.ModTime().After(cutoff) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(tmp, e.Name())); err != nil {
			return nil, errors.NewErr(tagTmpCleanFailed, err).SetMetadata("dir", tmp, "name", e.Name())
		}
	}
	return &Store{
		dir:    dir,
		tmp:    tmp,
		random: rand.Reader,
		create: func(dir, pattern string) (tempFile, error) {
			f, err := os.CreateTemp(dir, pattern)
			if err != nil {
				return nil, err // foreign: Save tags it
			}
			return f, nil
		},
	}, nil
}

// Save stores what r gives, at most limit bytes, under a new random key. The
// bytes go to a temporary file that is synced and renamed to its key, so a
// file under a key is always whole. A reader that gives one byte more than
// limit fails with TagTooLarge (metadata "limit"), a canceled ctx with
// TagCanceled; in every failure the temporary file is removed. Besides the
// size, Save finds the content type by sniffing and, for PNG, JPEG, GIF and
// WebP images, the dimensions (see Saved).
func (s *Store) Save(ctx context.Context, r io.Reader, limit int64) (Saved, error) {
	raw := make([]byte, keyBytes)
	if _, err := io.ReadFull(s.random, raw); err != nil {
		return Saved{}, errors.NewErr(tagKeyFailed, err)
	}
	key := hex.EncodeToString(raw)

	tmp, err := s.create(s.tmp, "upload-*")
	if err != nil {
		return Saved{}, errors.NewErr(tagTmpCreateFailed, err).SetMetadata("dir", s.tmp)
	}
	closed, done := false, false
	defer func() {
		if done {
			return
		}
		// Best effort: the error that made Save fail is the one that is
		// returned. What cannot be removed here is removed by a New that runs an hour
		// later.
		if !closed {
			_ = tmp.Close()
		}
		_ = os.Remove(tmp.Name())
	}()

	var size int64
	head := make([]byte, 0, sniffLength)
	buf := make([]byte, copyBufferSize)
	for {
		if err := ctx.Err(); err != nil {
			return Saved{}, errors.NewErr(tagCanceled, err)
		}
		n, rerr := r.Read(buf)
		if n > 0 {
			size += int64(n)
			if size > limit {
				return Saved{}, errors.New(TagTooLarge, "the file is larger than the limit", "limit", limit)
			}
			if room := sniffLength - len(head); room > 0 {
				head = append(head, buf[:min(n, room)]...)
			}
			if _, werr := tmp.Write(buf[:n]); werr != nil {
				return Saved{}, errors.NewErr(tagWriteFailed, werr).SetMetadata("dir", s.tmp)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return Saved{}, errors.NewErr(tagReadFailed, rerr)
		}
	}
	if err := tmp.Sync(); err != nil {
		return Saved{}, errors.NewErr(tagSyncFailed, err).SetMetadata("dir", s.tmp)
	}

	saved := Saved{Key: key, Size: size, ContentType: http.DetectContentType(head)}
	saved.ContentType, saved.Width, saved.Height = describe(saved.ContentType, head, tmp, size)

	closed = true
	if err := tmp.Close(); err != nil {
		return Saved{}, errors.NewErr(tagCloseFailed, err).SetMetadata("dir", s.tmp)
	}
	if err := os.Rename(tmp.Name(), filepath.Join(s.dir, key)); err != nil {
		return Saved{}, errors.NewErr(tagRenameFailed, err).SetMetadata("dir", s.dir)
	}
	done = true
	return saved, nil
}

// Open opens the file stored under key for reading; the caller closes it. A
// key that is not 64 lowercase hex characters fails with TagBadKey before the
// disk is touched, a key with no file with TagNotFound.
func (s *Store) Open(key string) (*os.File, error) {
	if !validKey(key) {
		return nil, errors.New(TagBadKey, "not a file key", "length", len(key))
	}
	f, err := os.Open(filepath.Join(s.dir, key))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, errors.NewErr(TagNotFound, err).SetMetadata("key", key)
		}
		return nil, errors.NewErr(tagOpenFailed, err).SetMetadata("key", key)
	}
	return f, nil
}

// Size returns the size in bytes of the file stored under key. A key that is
// not 64 lowercase hex characters fails with TagBadKey, a key with no file
// with TagNotFound.
func (s *Store) Size(key string) (int64, error) {
	if !validKey(key) {
		return 0, errors.New(TagBadKey, "not a file key", "length", len(key))
	}
	info, err := os.Stat(filepath.Join(s.dir, key))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, errors.NewErr(TagNotFound, err).SetMetadata("key", key)
		}
		return 0, errors.NewErr(tagStatFailed, err).SetMetadata("key", key)
	}
	return info.Size(), nil
}

// Remove deletes the file stored under key. A key with no file is not an
// error. A key that is not 64 lowercase hex characters fails with TagBadKey
// before the disk is touched.
func (s *Store) Remove(key string) error {
	if !validKey(key) {
		return errors.New(TagBadKey, "not a file key", "length", len(key))
	}
	if err := os.Remove(filepath.Join(s.dir, key)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return errors.NewErr(tagRemoveFailed, err).SetMetadata("key", key)
	}
	return nil
}

// validKey reports whether key has the form of a key that Save makes: 64
// lowercase hex characters. Nothing else can name a file of the store, so no
// key can reach outside its directory.
func validKey(key string) bool {
	if len(key) != keyLength {
		return false
	}
	for i := 0; i < len(key); i++ {
		c := key[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// describe returns the content type and the dimensions of a file: sniffed is
// its sniffed type, head its first bytes and f its content. For an image
// whose header cannot be read the type is "application/octet-stream".
func describe(sniffed string, head []byte, f io.ReaderAt, size int64) (contentType string, width, height int) {
	switch {
	case sniffed == "image/png" || sniffed == "image/jpeg" || sniffed == "image/gif":
		cfg, _, err := image.DecodeConfig(io.NewSectionReader(f, 0, size))
		if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
			return unknownType, 0, 0
		}
		return sniffed, cfg.Width, cfg.Height
	case sniffed == "image/webp" || isRIFFWebP(head):
		w, h, ok := webpSize(head)
		if !ok {
			return unknownType, 0, 0
		}
		return "image/webp", w, h
	default:
		return sniffed, 0, 0
	}
}

// isRIFFWebP reports whether head starts with the RIFF header of a WebP file.
func isRIFFWebP(head []byte) bool {
	return len(head) >= 12 && string(head[0:4]) == "RIFF" && string(head[8:12]) == "WEBP"
}

// webpSize reads the size in pixels from the first bytes of a WebP file: the
// header of its first chunk, lossy (VP8), lossless (VP8L) or extended (VP8X).
// ok is false when head is not such a header.
func webpSize(head []byte) (width, height int, ok bool) {
	const chunkStart = 20
	if !isRIFFWebP(head) || len(head) < chunkStart {
		return 0, 0, false
	}
	data := head[chunkStart:]
	// need is how many bytes of the chunk the size lies in.
	need := 10
	if string(head[12:16]) == "VP8L" {
		need = 5
	}
	if len(data) < need {
		return 0, 0, false
	}
	switch string(head[12:16]) {
	case "VP8 ":
		// A frame tag of 3 bytes, the start code 9d 01 2a, then the width
		// and the height in 14 bits each (the top 2 bits are a scale).
		if data[3] != 0x9d || data[4] != 0x01 || data[5] != 0x2a {
			return 0, 0, false
		}
		width = int(data[6]) | int(data[7]&0x3f)<<8
		height = int(data[8]) | int(data[9]&0x3f)<<8
	case "VP8L":
		// The signature 0x2f, then width-1 and height-1 in 14 bits each.
		if data[0] != 0x2f {
			return 0, 0, false
		}
		bits := uint32(data[1]) | uint32(data[2])<<8 | uint32(data[3])<<16 | uint32(data[4])<<24
		width = int(bits&0x3fff) + 1
		height = int(bits>>14&0x3fff) + 1
	case "VP8X":
		// Flags and reserved bytes, then the canvas width-1 and height-1 in
		// 24 bits each.
		width = (int(data[4]) | int(data[5])<<8 | int(data[6])<<16) + 1
		height = (int(data[7]) | int(data[8])<<8 | int(data[9])<<16) + 1
	default:
		return 0, 0, false
	}
	if width <= 0 || height <= 0 {
		return 0, 0, false
	}
	return width, height, true
}

const (
	// TagTooLarge is the tag of a file larger than the limit given to Save.
	// The metadata "limit" is the limit in bytes.
	TagTooLarge = "FileTooLarge"
	// TagBadKey is the tag of a key that is not 64 lowercase hex characters.
	TagBadKey = "FileKeyInvalid"
	// TagNotFound is the tag of a key with no file in the store.
	TagNotFound = "FileNotFound"

	tagDirCreateFailed = "FilesDirCreateFailed"
	tagTmpCleanFailed  = "FilesTmpCleanFailed"
	tagKeyFailed       = "FileKeyFailed"
	tagTmpCreateFailed = "FileTmpCreateFailed"
	tagCanceled        = "FileSaveCanceled"
	tagReadFailed      = "FileReadFailed"
	tagWriteFailed     = "FileWriteFailed"
	tagSyncFailed      = "FileSyncFailed"
	tagCloseFailed     = "FileCloseFailed"
	tagRenameFailed    = "FileRenameFailed"
	tagOpenFailed      = "FileOpenFailed"
	tagStatFailed      = "FileStatFailed"
	tagRemoveFailed    = "FileRemoveFailed"
)
