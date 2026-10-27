package files_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rm4n0s/clowder/internal/files"
	"github.com/rm4n0s/errors"
)

// newStore makes a store in a fresh temporary directory.
func newStore(t *testing.T) (*files.Store, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "files")
	s, err := files.New(dir)
	if err != nil {
		t.Fatalf("New: %+v", err)
	}
	return s, dir
}

// requireRoute fails the test unless err is a tagged error whose route
// contains route.
func requireRoute(t *testing.T, err error, route string) *errors.Error {
	t.Helper()
	appErr, ok := errors.FromError(err)
	if !ok {
		t.Fatalf("expected *errors.Error, got %T: %v", err, err)
	}
	if !appErr.HasRoute(route) {
		t.Fatalf("route = %s, want it to contain %s", appErr.Route(), route)
	}
	return appErr
}

// names returns the names in the directory.
func names(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	out := []string{}
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// skipIfRoot skips a test that needs the permission bits to count.
func skipIfRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits do not apply")
	}
}

func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func jpegBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := jpeg.Encode(&b, image.NewRGBA(image.Rect(0, 0, w, h)), nil); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func gifBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	var b bytes.Buffer
	img := image.NewPaletted(image.Rect(0, 0, w, h), color.Palette{color.Black, color.White})
	if err := gif.Encode(&b, img, nil); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// webp builds the start of a WebP file whose first chunk is kind with these
// payload bytes.
func webp(kind string, payload []byte) []byte {
	var b bytes.Buffer
	b.WriteString("RIFF")
	_ = binary.Write(&b, binary.LittleEndian, uint32(4+8+len(payload)))
	b.WriteString("WEBP")
	b.WriteString(kind)
	_ = binary.Write(&b, binary.LittleEndian, uint32(len(payload)))
	b.Write(payload)
	return b.Bytes()
}

// webpVP8 is a lossy file of w by h pixels.
func webpVP8(w, h int) []byte {
	p := []byte{0x10, 0x02, 0x00, 0x9d, 0x01, 0x2a, byte(w), byte(w >> 8), byte(h), byte(h >> 8), 0, 0}
	return webp("VP8 ", p)
}

// webpVP8L is a lossless file of w by h pixels.
func webpVP8L(w, h int) []byte {
	bits := uint32(w-1) | uint32(h-1)<<14
	p := []byte{0x2f, byte(bits), byte(bits >> 8), byte(bits >> 16), byte(bits >> 24), 0, 0}
	return webp("VP8L", p)
}

// webpVP8X is an extended file with a canvas of w by h pixels.
func webpVP8X(w, h int) []byte {
	p := []byte{0, 0, 0, 0, byte(w - 1), byte((w - 1) >> 8), byte((w - 1) >> 16), byte(h - 1), byte((h - 1) >> 8), byte((h - 1) >> 16)}
	return webp("VP8X", p)
}

func TestNew(t *testing.T) {
	t.Run("creates the directories", func(t *testing.T) {
		_, dir := newStore(t)
		for _, d := range []string{dir, filepath.Join(dir, "tmp")} {
			info, err := os.Stat(d)
			if err != nil || !info.IsDir() {
				t.Fatalf("Stat %s = %v, %v", d, info, err)
			}
		}
	})

	t.Run("removes what a crash left in tmp", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "files")
		tmp := filepath.Join(dir, "tmp")
		if err := os.MkdirAll(filepath.Join(tmp, "sub"), 0o750); err != nil {
			t.Fatal(err)
		}
		keep := filepath.Join(dir, strings.Repeat("a", 64))
		for _, f := range []string{filepath.Join(tmp, "upload-1"), filepath.Join(tmp, "sub", "x"), keep} {
			if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := files.New(dir); err != nil {
			t.Fatalf("New: %+v", err)
		}
		if got := names(t, tmp); len(got) != 0 {
			t.Errorf("tmp holds %v, want nothing", got)
		}
		if _, err := os.Stat(keep); err != nil {
			t.Errorf("a stored file was removed: %v", err)
		}
	})

	t.Run("a directory that cannot be made", func(t *testing.T) {
		blocker := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := files.New(filepath.Join(blocker, "files"))
		appErr := requireRoute(t, err, "New.FilesDirCreateFailed")
		if appErr.Tag != "FilesDirCreateFailed" {
			t.Errorf("tag = %s", appErr.Tag)
		}
	})

	t.Run("a tmp that cannot be read", func(t *testing.T) {
		skipIfRoot(t)
		dir := filepath.Join(t.TempDir(), "files")
		tmp := filepath.Join(dir, "tmp")
		if err := os.MkdirAll(tmp, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(tmp, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(tmp, 0o750) })
		_, err := files.New(dir)
		_ = requireRoute(t, err, "New.FilesTmpCleanFailed")
	})

	t.Run("a leftover that cannot be removed", func(t *testing.T) {
		skipIfRoot(t)
		dir := filepath.Join(t.TempDir(), "files")
		tmp := filepath.Join(dir, "tmp")
		sub := filepath.Join(tmp, "sub")
		if err := os.MkdirAll(sub, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sub, "x"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(sub, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(sub, 0o750) })
		_, err := files.New(dir)
		_ = requireRoute(t, err, "New.FilesTmpCleanFailed")
	})
}

func TestSaveOpenRemove(t *testing.T) {
	s, dir := newStore(t)
	ctx := context.Background()
	content := "hello, files\n"

	saved, err := s.Save(ctx, strings.NewReader(content), 100)
	if err != nil {
		t.Fatalf("Save: %+v", err)
	}
	if len(saved.Key) != 64 || strings.ToLower(saved.Key) != saved.Key {
		t.Errorf("Key = %q, want 64 lowercase hex characters", saved.Key)
	}
	if saved.Size != int64(len(content)) || saved.ContentType != "text/plain; charset=utf-8" || saved.Width != 0 || saved.Height != 0 {
		t.Errorf("Saved = %+v", saved)
	}
	if got := names(t, dir); len(got) != 2 || got[0] != saved.Key && got[1] != saved.Key {
		t.Errorf("directory holds %v", got)
	}
	if got := names(t, filepath.Join(dir, "tmp")); len(got) != 0 {
		t.Errorf("tmp holds %v after a save", got)
	}

	f, err := s.Open(saved.Key)
	if err != nil {
		t.Fatalf("Open: %+v", err)
	}
	b, err := io.ReadAll(f)
	if cerr := f.Close(); cerr != nil || err != nil {
		t.Fatalf("ReadAll, Close = %v, %v", err, cerr)
	}
	if string(b) != content {
		t.Errorf("content = %q", b)
	}

	if err := s.Remove(saved.Key); err != nil {
		t.Fatalf("Remove: %+v", err)
	}
	if _, err := s.Open(saved.Key); err == nil {
		t.Error("Open after Remove succeeded")
	}
	if err := s.Remove(saved.Key); err != nil {
		t.Errorf("Remove of a missing file = %+v, want nil", err)
	}

	a, err := s.Save(ctx, strings.NewReader("x"), 10)
	if err != nil {
		t.Fatal(err)
	}
	b2, err := s.Save(ctx, strings.NewReader("x"), 10)
	if err != nil {
		t.Fatal(err)
	}
	if a.Key == b2.Key {
		t.Error("two saves got the same key")
	}
}

func TestSaveEmptyFile(t *testing.T) {
	s, _ := newStore(t)
	saved, err := s.Save(context.Background(), strings.NewReader(""), 10)
	if err != nil {
		t.Fatalf("Save: %+v", err)
	}
	if saved.Size != 0 || saved.ContentType != "text/plain; charset=utf-8" {
		t.Errorf("Saved = %+v", saved)
	}
}

func TestSaveLimit(t *testing.T) {
	for _, tc := range []struct {
		name    string
		size    int
		limit   int64
		tooBig  bool
		storeds int
	}{
		{"one byte under", 99, 100, false, 1},
		{"exactly at the limit", 100, 100, false, 1},
		{"one byte over", 101, 100, true, 0},
		{"many chunks at the limit", 100_000, 100_000, false, 1},
		{"many chunks one over", 100_001, 100_000, true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, dir := newStore(t)
			saved, err := s.Save(context.Background(), bytes.NewReader(bytes.Repeat([]byte("a"), tc.size)), tc.limit)
			if tc.tooBig {
				appErr := requireRoute(t, err, "Store.Save.FileTooLarge")
				if appErr.Tag != files.TagTooLarge || appErr.Metadata["limit"] != tc.limit {
					t.Errorf("error = %s %v", appErr.Tag, appErr.Metadata)
				}
			} else if err != nil || saved.Size != int64(tc.size) {
				t.Fatalf("Save = %+v, %v", saved, err)
			}
			if got := names(t, filepath.Join(dir, "tmp")); len(got) != 0 {
				t.Errorf("tmp holds %v", got)
			}
			if got := names(t, dir); len(got) != tc.storeds+1 {
				t.Errorf("directory holds %v, want %d stored files", got, tc.storeds)
			}
		})
	}
}

func TestSaveSniffsTheTypeAndTheSize(t *testing.T) {
	garbage := []byte{0x00, 0x01, 0x02, 0xff, 0xfe, 0x10}
	for _, tc := range []struct {
		name          string
		data          []byte
		contentType   string
		width, height int
	}{
		{"png", pngBytes(t, 320, 200), "image/png", 320, 200},
		{"png 1x1", pngBytes(t, 1, 1), "image/png", 1, 1},
		{"jpeg", jpegBytes(t, 64, 48), "image/jpeg", 64, 48},
		{"gif", gifBytes(t, 33, 17), "image/gif", 33, 17},
		{"webp lossy", webpVP8(640, 480), "image/webp", 640, 480},
		{"webp lossless", webpVP8L(1000, 333), "image/webp", 1000, 333},
		{"webp extended", webpVP8X(4000, 3000), "image/webp", 4000, 3000},
		{"webp lossy scale bits ignored", func() []byte {
			b := webpVP8(100, 50)
			b[27] |= 0xc0
			b[29] |= 0xc0
			return b
		}(), "image/webp", 100, 50},
		{"text", []byte("just some words\n"), "text/plain; charset=utf-8", 0, 0},
		{"html is only text to us", []byte("<!DOCTYPE html><html><body>hi</body></html>"), "text/html; charset=utf-8", 0, 0},
		{"pdf", []byte("%PDF-1.4\n%\xe2\xe3\xcf\xd3\n"), "application/pdf", 0, 0},
		{"binary", garbage, "application/octet-stream", 0, 0},
		{"a png header that is cut", pngBytes(t, 20, 20)[:12], "application/octet-stream", 0, 0},
		{"a jpeg that is cut", jpegBytes(t, 20, 20)[:4], "application/octet-stream", 0, 0},
		{"a webp that is cut", webpVP8(10, 10)[:20], "application/octet-stream", 0, 0},
		{"a webp lossy with a wrong start code", func() []byte {
			b := webpVP8(10, 10)
			b[23] = 0
			return b
		}(), "application/octet-stream", 0, 0},
		{"a webp lossless with a wrong signature", func() []byte {
			b := webpVP8L(10, 10)
			b[20] = 0
			return b
		}(), "application/octet-stream", 0, 0},
		{"a webp with an unknown chunk", webp("ABCD", make([]byte, 16)), "application/octet-stream", 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newStore(t)
			saved, err := s.Save(context.Background(), bytes.NewReader(tc.data), 1<<20)
			if err != nil {
				t.Fatalf("Save: %+v", err)
			}
			if saved.ContentType != tc.contentType || saved.Width != tc.width || saved.Height != tc.height || saved.Size != int64(len(tc.data)) {
				t.Errorf("Saved = %+v, want %s %dx%d size %d", saved, tc.contentType, tc.width, tc.height, len(tc.data))
			}
		})
	}
}

// A JPEG whose size comes after a long comment segment is past the 512 bytes
// that are sniffed; the dimensions still come out.
func TestSaveFindsTheSizeOfAJPEGWithALongHeader(t *testing.T) {
	base := jpegBytes(t, 40, 30)
	comment := append([]byte{0xff, 0xfe, 0x04, 0x00}, bytes.Repeat([]byte("c"), 1024)...)
	binary.BigEndian.PutUint16(comment[2:4], uint16(len(comment)-2))
	data := append(append(append([]byte{}, base[:2]...), comment...), base[2:]...)
	s, _ := newStore(t)
	saved, err := s.Save(context.Background(), bytes.NewReader(data), 1<<20)
	if err != nil {
		t.Fatalf("Save: %+v", err)
	}
	if saved.ContentType != "image/jpeg" || saved.Width != 40 || saved.Height != 30 {
		t.Errorf("Saved = %+v", saved)
	}
}

func TestBadKeys(t *testing.T) {
	s, dir := newStore(t)
	outside := filepath.Join(filepath.Dir(dir), "outside")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	good := strings.Repeat("ab", 32)
	for _, tc := range []struct {
		name string
		key  string
	}{
		{"empty", ""},
		{"traversal", "../outside"},
		{"traversal with a long name", "../" + strings.Repeat("a", 61)},
		{"a path", "tmp/" + strings.Repeat("a", 60)},
		{"short", "abcdef"},
		{"one short", good[:63]},
		{"one long", good + "a"},
		{"uppercase", strings.ToUpper(good)},
		{"not hex", strings.Repeat("g", 64)},
		{"a space", good[:63] + " "},
		{"a NUL", good[:63] + "\x00"},
		{"a dot", strings.Repeat(".", 64)},
		{"a backslash", good[:63] + `\`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, err := s.Open(tc.key)
			if f != nil {
				_ = f.Close()
				t.Fatal("Open returned a file")
			}
			appErr := requireRoute(t, err, "Store.Open.FileKeyInvalid")
			if appErr.Tag != files.TagBadKey {
				t.Errorf("Open tag = %s", appErr.Tag)
			}
			err = s.Remove(tc.key)
			appErr = requireRoute(t, err, "Store.Remove.FileKeyInvalid")
			if appErr.Tag != files.TagBadKey {
				t.Errorf("Remove tag = %s", appErr.Tag)
			}
		})
	}
	if b, err := os.ReadFile(outside); err != nil || string(b) != "secret" {
		t.Errorf("the file outside the store changed: %q, %v", b, err)
	}
}

func TestOpenAMissingKey(t *testing.T) {
	s, _ := newStore(t)
	_, err := s.Open(strings.Repeat("0", 64))
	appErr := requireRoute(t, err, "Store.Open.FileNotFound")
	if appErr.Tag != files.TagNotFound {
		t.Errorf("tag = %s", appErr.Tag)
	}
}

func TestOpenFailsWhenTheFileCannotBeRead(t *testing.T) {
	skipIfRoot(t)
	s, dir := newStore(t)
	key := strings.Repeat("1", 64)
	if err := os.WriteFile(filepath.Join(dir, key), []byte("x"), 0); err != nil {
		t.Fatal(err)
	}
	_, err := s.Open(key)
	_ = requireRoute(t, err, "Store.Open.FileOpenFailed")
}

func TestRemoveFailsWhenTheFileCannotBeRemoved(t *testing.T) {
	s, dir := newStore(t)
	key := strings.Repeat("2", 64)
	if err := os.MkdirAll(filepath.Join(dir, key, "inner"), 0o750); err != nil {
		t.Fatal(err)
	}
	err := s.Remove(key)
	_ = requireRoute(t, err, "Store.Remove.FileRemoveFailed")
}

// slowReader gives its data in small pieces and runs after on each Read.
type slowReader struct {
	data  []byte
	after func(reads int)
	reads int
}

func (r *slowReader) Read(p []byte) (int, error) {
	r.reads++
	if r.after != nil {
		r.after(r.reads)
	}
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	n := copy(p[:min(len(p), 10)], r.data)
	r.data = r.data[n:]
	return n, nil
}

func TestSaveStopsWhenTheContextIsCanceled(t *testing.T) {
	t.Run("before the first read", func(t *testing.T) {
		s, dir := newStore(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := s.Save(ctx, strings.NewReader("data"), 100)
		appErr := requireRoute(t, err, "Store.Save.FileSaveCanceled")
		if !errors.Is(err, context.Canceled) {
			t.Errorf("the error does not hold context.Canceled: %v", appErr.OriginalErr)
		}
		if got := names(t, filepath.Join(dir, "tmp")); len(got) != 0 {
			t.Errorf("tmp holds %v", got)
		}
	})

	t.Run("in the middle", func(t *testing.T) {
		s, dir := newStore(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		r := &slowReader{data: bytes.Repeat([]byte("a"), 100), after: func(n int) {
			if n == 3 {
				cancel()
			}
		}}
		_, err := s.Save(ctx, r, 1000)
		_ = requireRoute(t, err, "Store.Save.FileSaveCanceled")
		if r.reads > 4 {
			t.Errorf("Save read %d times after the cancel", r.reads)
		}
		if got := names(t, filepath.Join(dir, "tmp")); len(got) != 0 {
			t.Errorf("tmp holds %v", got)
		}
		if got := names(t, dir); len(got) != 1 {
			t.Errorf("directory holds %v, want only tmp", got)
		}
	})
}

// failingFile is a temporary file in which one step fails.
type failingFile struct {
	files.TempFile
	failWrite, failSync, failClose bool
}

func (f *failingFile) Write(p []byte) (int, error) {
	if f.failWrite {
		return 0, os.ErrInvalid
	}
	return f.TempFile.Write(p)
}

func (f *failingFile) Sync() error {
	if f.failSync {
		return os.ErrInvalid
	}
	return f.TempFile.Sync()
}

func (f *failingFile) Close() error {
	err := f.TempFile.Close()
	if f.failClose {
		return os.ErrInvalid
	}
	return err
}

// errReader fails on its first Read.
type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, os.ErrInvalid }

func TestSaveFailures(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(s *files.Store, dir string)
		r     io.Reader
		route string
	}{
		{"the key cannot be made", func(s *files.Store, _ string) { s.UseRandom(strings.NewReader("short")) },
			strings.NewReader("x"), "Store.Save.FileKeyFailed"},
		{"the temporary file cannot be made", func(_ *files.Store, dir string) {
			if err := os.RemoveAll(filepath.Join(dir, "tmp")); err != nil {
				panic(err)
			}
		}, strings.NewReader("x"), "Store.Save.FileTmpCreateFailed"},
		{"the reader fails", nil, errReader{}, "Store.Save.FileReadFailed"},
		{"the write fails", func(s *files.Store, _ string) {
			s.UseCreate(func(dir, pattern string) (files.TempFile, error) {
				f, err := files.CreateTemp(dir, pattern)
				return &failingFile{TempFile: f, failWrite: true}, err
			})
		}, strings.NewReader("x"), "Store.Save.FileWriteFailed"},
		{"the sync fails", func(s *files.Store, _ string) {
			s.UseCreate(func(dir, pattern string) (files.TempFile, error) {
				f, err := files.CreateTemp(dir, pattern)
				return &failingFile{TempFile: f, failSync: true}, err
			})
		}, strings.NewReader("x"), "Store.Save.FileSyncFailed"},
		{"the close fails", func(s *files.Store, _ string) {
			s.UseCreate(func(dir, pattern string) (files.TempFile, error) {
				f, err := files.CreateTemp(dir, pattern)
				return &failingFile{TempFile: f, failClose: true}, err
			})
		}, strings.NewReader("x"), "Store.Save.FileCloseFailed"},
		{"the rename fails", func(s *files.Store, dir string) {
			fixed := bytes.Repeat([]byte{0xab}, 32)
			s.UseRandom(bytes.NewReader(fixed))
			// A directory already has the name of the key.
			if err := os.MkdirAll(filepath.Join(dir, strings.Repeat("ab", 32), "inner"), 0o750); err != nil {
				panic(err)
			}
		}, strings.NewReader("x"), "Store.Save.FileRenameFailed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, dir := newStore(t)
			if tc.setup != nil {
				tc.setup(s, dir)
			}
			_, err := s.Save(context.Background(), tc.r, 100)
			_ = requireRoute(t, err, tc.route)
			if entries, rerr := os.ReadDir(filepath.Join(dir, "tmp")); rerr == nil && len(entries) != 0 {
				t.Errorf("tmp holds %d files after a failed save", len(entries))
			}
		})
	}
}

func TestSize(t *testing.T) {
	s, _ := newStore(t)
	saved, err := s.Save(context.Background(), strings.NewReader("12345"), 100)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := s.Size(saved.Key); err != nil || n != 5 {
		t.Errorf("Size = %d, %v", n, err)
	}
	_, err = s.Size(strings.Repeat("0", 64))
	appErr := requireRoute(t, err, "Store.Size.FileNotFound")
	if appErr.Tag != files.TagNotFound {
		t.Errorf("tag = %s", appErr.Tag)
	}
	_, err = s.Size("../x")
	appErr = requireRoute(t, err, "Store.Size.FileKeyInvalid")
	if appErr.Tag != files.TagBadKey {
		t.Errorf("tag = %s", appErr.Tag)
	}
}

func TestSizeFailsWhenTheDiskDoes(t *testing.T) {
	skipIfRoot(t)
	s, dir := newStore(t)
	saved, err := s.Save(context.Background(), strings.NewReader("x"), 100)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o750) })
	_, err = s.Size(saved.Key)
	_ = requireRoute(t, err, "Store.Size.FileStatFailed")
}
