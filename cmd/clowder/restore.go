package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/rm4n0s/clowder/internal/config"
	"github.com/rm4n0s/clowder/internal/store"
	"github.com/rm4n0s/errors"
)

// maxEntryCount caps the entries a manifest may list.
const maxEntryCount = 50_000_000

// restored is what a restore made, for its summary.
type restored struct {
	manifest backupManifest
	files    int
	dirs     int
	bytes    int64
}

// runRestore unpacks the archive made by backup into an empty data directory
// (SPEC D59). It checks the manifest and every entry before it trusts them,
// writes into a temporary directory inside the target, and moves the result
// into place only when the whole archive was read and verified; on any failure
// the target is as it was. stdin is read when -i is "-".
func runRestore(ctx context.Context, args []string, env func(string) string, stdout, stderr io.Writer, stdin io.Reader) error {
	dataDir := envString(env, "CLOWDER_DATA", config.DefaultDataDir)
	var input string
	flags := flag.NewFlagSet("restore", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&input, "i", "", "the archive to read, made by clowder backup; - reads it from the standard input")
	flags.StringVar(&dataDir, "data", dataDir, "data directory to restore into; it must not exist or be empty (CLOWDER_DATA)")
	if err := flags.Parse(args); err != nil {
		return errors.NewErr(tagFlagsInvalid, err)
	}
	if flags.NArg() > 0 {
		return errors.New(tagFlagsInvalid, "unexpected arguments", "args", flags.Args())
	}
	if input == "" {
		return errors.New(tagRestoreInputInvalid, "give the archive to read with -i (a file name, or - for the standard input)")
	}
	if dataDir == "" {
		return errors.New(tagRestoreTargetInvalid, "the data directory is empty")
	}
	target, err := filepath.Abs(dataDir)
	if err != nil {
		return errors.NewErr(tagRestoreTargetInvalid, err).SetMetadata("dir", dataDir)
	}

	in := stdin
	if input != stdStream {
		f, err := os.Open(input)
		if err != nil {
			return errors.NewErr(tagRestoreInputInvalid, err).SetMetadata("path", input)
		}
		defer func() { _ = f.Close() }()
		in = f
	}

	created, err := prepareRestoreTarget(target)
	if err != nil {
		return err
	}
	res, err := restoreInto(ctx, in, target)
	if err != nil {
		if created != "" {
			// The target did not exist: leave it as it was.
			_ = os.RemoveAll(created)
		}
		return err
	}
	reportRestore(stdout, target, res)
	return nil
}

// prepareRestoreTarget checks that the target is a directory that does not
// exist or is empty, and makes it (with mode 0700) when it does not exist. It
// returns the topmost directory it made, or "" when it made none. A target
// with anything in it is refused before a byte is written.
func prepareRestoreTarget(target string) (created string, err error) {
	info, err := os.Stat(target)
	switch {
	case err == nil:
		if !info.IsDir() {
			return "", errors.New(tagRestoreTargetInvalid, "the target is not a directory", "dir", target)
		}
		entries, err := os.ReadDir(target)
		if err != nil {
			return "", errors.NewErr(tagRestoreTargetInvalid, err).SetMetadata("dir", target)
		}
		if len(entries) > 0 {
			return "", errors.NewErrf(tagRestoreTargetNotEmpty, "restore only into an empty directory: %s has %d entries (%s ...)", target, len(entries), entries[0].Name()).
				SetMetadata("dir", target, "entries", len(entries), "first", entries[0].Name())
		}
		return "", nil
	case !errors.Is(err, fs.ErrNotExist):
		return "", errors.NewErr(tagRestoreTargetInvalid, err).SetMetadata("dir", target)
	}
	// The topmost directory that is missing, to remove it if the restore fails.
	top := target
	for {
		parent := filepath.Dir(top)
		if _, err := os.Stat(parent); err == nil || parent == top {
			break
		}
		top = parent
	}
	if err := os.MkdirAll(target, backupDirMode); err != nil {
		return "", errors.NewErr(tagRestoreTargetInvalid, err).SetMetadata("dir", target)
	}
	return top, nil
}

// restoreInto reads the archive from in, verifies it and moves its entries into
// target, which is empty. The entries are written first into a directory made
// inside target, which is removed whatever happens.
func restoreInto(ctx context.Context, in io.Reader, target string) (restored, error) {
	stage, err := os.MkdirTemp(target, ".restore-*")
	if err != nil {
		return restored{}, errors.NewErr(tagRestoreWriteFailed, err).SetMetadata("dir", target, "step", "stage")
	}
	defer func() { _ = os.RemoveAll(stage) }()
	res, err := unpackArchive(ctx, in, stage)
	if err != nil {
		return restored{}, err
	}
	names, err := os.ReadDir(stage)
	if err != nil {
		return restored{}, errors.NewErr(tagRestoreWriteFailed, err).SetMetadata("dir", stage, "step", "list")
	}
	var moved []string
	for _, e := range names {
		dest := filepath.Join(target, e.Name())
		if err := os.Rename(filepath.Join(stage, e.Name()), dest); err != nil {
			for _, m := range moved {
				_ = os.RemoveAll(m)
			}
			return restored{}, errors.NewErr(tagRestoreWriteFailed, err).SetMetadata("path", dest, "step", "move")
		}
		moved = append(moved, dest)
	}
	return res, nil
}

// capReader is the decompressed archive with a cap on its size: a read that
// passes the limit fails and sets exceeded.
type capReader struct {
	r        io.Reader
	read     int64
	limit    int64
	exceeded bool
}

// capExceeded is the error of a capReader that went past its limit.
type capExceeded struct{}

// Error describes the failure.
func (capExceeded) Error() string { return "the archive is larger than its manifest says" }

// Read reads from the archive, and fails once more than the limit was read.
func (c *capReader) Read(p []byte) (int, error) {
	if c.exceeded {
		return 0, capExceeded{}
	}
	if room := c.limit - c.read + 1; int64(len(p)) > room {
		p = p[:room]
	}
	n, err := c.r.Read(p)
	c.read += int64(n)
	if c.read > c.limit {
		c.exceeded = true
		return n - int(c.read-c.limit), capExceeded{}
	}
	return n, err
}

// unpackArchive reads the archive from in and writes its entries under stage,
// verifying all of it: the manifest first, then every entry against it. It
// returns what it wrote.
func unpackArchive(ctx context.Context, in io.Reader, stage string) (restored, error) {
	gz, err := gzip.NewReader(in)
	if err != nil {
		return restored{}, errors.NewErr(tagRestoreArchiveInvalid, err).SetMetadata("step", "gzip")
	}
	defer func() { _ = gz.Close() }()
	cr := &capReader{r: gz, limit: maxManifestBytes + archiveMargin}
	tr := tar.NewReader(cr)

	m, err := readManifest(tr, cr)
	if err != nil {
		return restored{}, err
	}
	listed, total, err := checkManifest(m)
	if err != nil {
		return restored{}, err
	}

	// From here the archive may have what the manifest accounts for: the
	// content of its files, a header and padding for each entry (with room for
	// an extended header), and a margin.
	cr.limit = cr.read + total + int64(len(listed))*4*tarBlock + archiveMargin
	res := restored{manifest: m}
	seen := make(map[string]bool, len(listed))
	for {
		if err := ctx.Err(); err != nil {
			return restored{}, errors.NewErr(tagRestoreWriteFailed, err).SetMetadata("step", "canceled")
		}
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			if cr.exceeded {
				return restored{}, errors.New(tagRestoreArchiveTooLarge, "the archive is larger than its manifest says")
			}
			return restored{}, errors.NewErr(tagRestoreArchiveInvalid, err).SetMetadata("step", "entry")
		}
		var isDir bool
		switch hdr.Typeflag {
		case tar.TypeReg:
		case tar.TypeDir:
			isDir = true
		default:
			return restored{}, errors.NewErrf(tagRestoreEntryUnsafe, "the archive has an entry that is neither a file nor a directory: %s (type %q)",
				hdr.Name, string(hdr.Typeflag)).SetMetadata("path", hdr.Name, "type", string(hdr.Typeflag))
		}
		rel, found := strings.CutPrefix(hdr.Name, backupTopDir+"/")
		if !found {
			return restored{}, errors.NewErrf(tagRestoreEntryUnsafe, "an entry is not under the top directory %s: %s", backupTopDir, hdr.Name).
				SetMetadata("path", hdr.Name, "reason", "outside "+backupTopDir)
		}
		if isDir {
			rel = strings.TrimSuffix(rel, "/")
		}
		if reason := unsafePath(rel); reason != "" {
			return restored{}, errors.NewErrf(tagRestoreEntryUnsafe, "an entry has an unsafe path (%s): %s", reason, hdr.Name).
				SetMetadata("path", hdr.Name, "reason", reason)
		}
		if seen[rel] {
			return restored{}, errors.NewErrf(tagRestoreEntryDuplicate, "the archive has an entry twice: %s", rel).SetMetadata("path", rel)
		}
		want, ok := listed[rel]
		if !ok || (want.Type == entryDir) != isDir {
			return restored{}, errors.NewErrf(tagRestoreEntryUnlisted, "the archive has an entry that its manifest does not list: %s", rel).
				SetMetadata("path", rel)
		}
		seen[rel] = true
		dest := filepath.Join(stage, filepath.FromSlash(rel))
		if isDir {
			if err := os.MkdirAll(dest, backupDirMode); err != nil {
				return restored{}, errors.NewErr(tagRestoreWriteFailed, err).SetMetadata("path", rel, "step", "mkdir")
			}
			if err := os.Chmod(dest, backupDirMode); err != nil {
				return restored{}, errors.NewErr(tagRestoreWriteFailed, err).SetMetadata("path", rel, "step", "chmod")
			}
			res.dirs++
			continue
		}
		if hdr.Size != want.Size {
			return restored{}, errors.NewErrf(tagRestoreSizeMismatch, "an entry has another size than its manifest says: %s (%d bytes, the manifest says %d)",
				rel, hdr.Size, want.Size).SetMetadata("path", rel, "size", hdr.Size, "declared", want.Size)
		}
		if err := extractFile(ctx, tr, cr, dest, want); err != nil {
			return restored{}, err
		}
		res.files++
		res.bytes += want.Size
	}
	// Whatever follows the end of the tar stream is read, which also checks the
	// checksum of the gzip stream, and may not be more than the margin.
	if _, err := io.Copy(io.Discard, ctxReader{ctx: ctx, r: cr}); err != nil {
		if cr.exceeded {
			return restored{}, errors.New(tagRestoreArchiveTooLarge, "the archive is larger than its manifest says")
		}
		return restored{}, errors.NewErr(tagRestoreArchiveInvalid, err).SetMetadata("step", "end")
	}
	for _, e := range m.Entries {
		if !seen[e.Path] {
			return restored{}, errors.NewErrf(tagRestoreEntryMissing, "the manifest lists an entry that the archive does not have: %s", e.Path).
				SetMetadata("path", e.Path)
		}
	}
	return res, nil
}

// extractFile writes the content of the entry that tr is at to dest, with the
// mode 0600, and checks its size and SHA-256 against want. The tar reader
// gives no more than the size the header declares, and the header was checked
// against the manifest.
func extractFile(ctx context.Context, tr *tar.Reader, cr *capReader, dest string, want backupEntry) error {
	if err := os.MkdirAll(filepath.Dir(dest), backupDirMode); err != nil {
		return errors.NewErr(tagRestoreWriteFailed, err).SetMetadata("path", want.Path, "step", "mkdir")
	}
	f, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, backupFileMode)
	if err != nil {
		return errors.NewErr(tagRestoreWriteFailed, err).SetMetadata("path", want.Path, "step", "create")
	}
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(f, h), ctxReader{ctx: ctx, r: tr})
	chmodErr := f.Chmod(backupFileMode)
	closeErr := f.Close()
	switch {
	case copyErr != nil && cr.exceeded:
		return errors.New(tagRestoreArchiveTooLarge, "the archive is larger than its manifest says", "path", want.Path)
	case copyErr != nil:
		return errors.NewErr(tagRestoreArchiveInvalid, copyErr).SetMetadata("path", want.Path, "step", "content")
	case chmodErr != nil:
		return errors.NewErr(tagRestoreWriteFailed, chmodErr).SetMetadata("path", want.Path, "step", "chmod")
	case closeErr != nil:
		return errors.NewErr(tagRestoreWriteFailed, closeErr).SetMetadata("path", want.Path, "step", "close")
	case n != want.Size:
		return errors.NewErrf(tagRestoreSizeMismatch, "an entry has another size than its manifest says: %s (%d bytes, the manifest says %d)",
			want.Path, n, want.Size).SetMetadata("path", want.Path, "size", n, "declared", want.Size)
	case hex.EncodeToString(h.Sum(nil)) != want.SHA256:
		return errors.NewErrf(tagRestoreChecksumMismatch, "the content of an entry is not what its manifest says: %s", want.Path).
			SetMetadata("path", want.Path)
	}
	return nil
}

// readManifest reads the first entry of the archive, which must be the
// manifest, and decodes it.
func readManifest(tr *tar.Reader, cr *capReader) (backupManifest, error) {
	hdr, err := tr.Next()
	if err != nil {
		if cr.exceeded {
			return backupManifest{}, errors.New(tagRestoreArchiveTooLarge, "the archive is larger than its manifest says")
		}
		if errors.Is(err, io.EOF) {
			return backupManifest{}, errors.New(tagRestoreManifestNotFirst, "the archive is empty")
		}
		return backupManifest{}, errors.NewErr(tagRestoreArchiveInvalid, err).SetMetadata("step", "first entry")
	}
	if hdr.Typeflag != tar.TypeReg || hdr.Name != backupTopDir+"/"+backupManifestName {
		return backupManifest{}, errors.New(tagRestoreManifestNotFirst, "the first entry of the archive is not its manifest",
			"first", hdr.Name)
	}
	if hdr.Size > maxManifestBytes {
		return backupManifest{}, errors.New(tagRestoreManifestInvalid, "the manifest is too large", "size", hdr.Size)
	}
	raw, err := io.ReadAll(tr)
	if err != nil {
		return backupManifest{}, errors.NewErr(tagRestoreArchiveInvalid, err).SetMetadata("step", "manifest")
	}
	var m backupManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return backupManifest{}, errors.NewErr(tagRestoreManifestInvalid, err).SetMetadata("step", "json")
	}
	return m, nil
}

// checkManifest checks the manifest before anything is trusted: the format is
// known, the schema is not newer than this binary's migrations, and every
// entry is a file or a directory with a safe path in a place a data directory
// has, once, under a directory that is listed, with a size and a SHA-256. It
// returns the entries by path and the sum of the sizes of the files.
func checkManifest(m backupManifest) (map[string]backupEntry, int64, error) {
	if m.Format != backupFormat {
		return nil, 0, errors.NewErrf(tagRestoreFormatUnknown, "the format %d of the archive is not one this Clowder reads (it reads %d)", m.Format, backupFormat).
			SetMetadata("format", m.Format, "known", backupFormat)
	}
	if latest := store.LatestMigration(); m.Schema > latest {
		return nil, 0, errors.NewErrf(tagRestoreSchemaTooNew, "the archive is from a newer Clowder: its schema is %s, this Clowder knows up to %s", m.Schema, latest).
			SetMetadata("schema", m.Schema, "known", latest)
	}
	switch m.Database {
	case databaseFile, databaseExternal:
	default:
		return nil, 0, errors.New(tagRestoreManifestInvalid, "the database of the manifest is unknown", "database", m.Database)
	}
	if len(m.Entries) > maxEntryCount {
		return nil, 0, errors.New(tagRestoreManifestInvalid, "the manifest lists too many entries", "entries", len(m.Entries))
	}
	listed := make(map[string]backupEntry, len(m.Entries))
	var total int64
	for _, e := range m.Entries {
		if reason := unsafePath(e.Path); reason != "" {
			return nil, 0, errors.NewErrf(tagRestoreManifestInvalid, "the manifest lists an entry with an unsafe path (%s): %s", reason, e.Path).
				SetMetadata("path", e.Path, "reason", reason)
		}
		if _, dup := listed[e.Path]; dup {
			return nil, 0, errors.NewErrf(tagRestoreManifestInvalid, "the manifest lists a path twice: %s", e.Path).SetMetadata("path", e.Path)
		}
		if !allowedPlace(e, m.Database) {
			return nil, 0, errors.NewErrf(tagRestoreManifestInvalid, "the manifest lists an entry that has no place in a data directory: %s", e.Path).
				SetMetadata("path", e.Path, "type", e.Type)
		}
		switch e.Type {
		case entryDir:
			if e.Size != 0 || e.SHA256 != "" {
				return nil, 0, errors.NewErrf(tagRestoreManifestInvalid, "a directory has a size or a checksum: %s", e.Path).SetMetadata("path", e.Path)
			}
		case entryFile:
			if e.Size < 0 || e.Size > maxEntryBytes || total+e.Size > 16*maxEntryBytes {
				return nil, 0, errors.NewErrf(tagRestoreManifestInvalid, "an entry declares an impossible size: %s (%d)", e.Path, e.Size).
					SetMetadata("path", e.Path, "size", e.Size)
			}
			if !validSHA256(e.SHA256) {
				return nil, 0, errors.NewErrf(tagRestoreManifestInvalid, "an entry has no valid SHA-256: %s", e.Path).SetMetadata("path", e.Path)
			}
			total += e.Size
		default:
			return nil, 0, errors.NewErrf(tagRestoreManifestInvalid, "an entry has an unknown type %q: %s", e.Type, e.Path).SetMetadata("path", e.Path, "type", e.Type)
		}
		listed[e.Path] = e
	}
	for p, e := range listed {
		if dir := parentOf(p); dir != "" && listed[dir].Type != entryDir {
			return nil, 0, errors.NewErrf(tagRestoreManifestInvalid, "the manifest lists an entry whose directory is not listed: %s", p).
				SetMetadata("path", p, "type", e.Type)
		}
	}
	if m.Database == databaseFile && listed[databaseFile].Type != entryFile {
		return nil, 0, errors.New(tagRestoreManifestInvalid, "the manifest names a database that it does not list",
			"database", m.Database)
	}
	return listed, total, nil
}

// unsafePath says why p cannot be a relative path inside a directory, or
// returns "" when it can: a path that is empty or absolute, has an empty, "."
// or ".." element, or has a backslash or a NUL.
func unsafePath(p string) string {
	switch {
	case p == "":
		return "empty path"
	case strings.HasPrefix(p, "/"):
		return "absolute path"
	case strings.ContainsAny(p, "\\\x00"):
		return "backslash or NUL"
	}
	for _, el := range strings.Split(p, "/") {
		switch el {
		case "":
			return "empty element"
		case ".":
			return "element ."
		case "..":
			return "element .."
		}
	}
	return ""
}

// allowedPlace reports whether e may be written into a data directory: the
// database file when the archive holds the database, the SSH host key, and
// the files directory with everything under it.
func allowedPlace(e backupEntry, database string) bool {
	switch e.Path {
	case databaseFile:
		return database == databaseFile && e.Type == entryFile
	case backupHostKeyName:
		return e.Type == entryFile
	case filesDir:
		return e.Type == entryDir
	}
	return strings.HasPrefix(e.Path, filesDir+"/")
}

// parentOf returns the directory part of a slash path, "" for a name at the
// top.
func parentOf(p string) string {
	i := strings.LastIndexByte(p, '/')
	if i < 0 {
		return ""
	}
	return p[:i]
}

// validSHA256 reports whether s is a lower-case hex SHA-256.
func validSHA256(s string) bool {
	if len(s) != 2*sha256.Size {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// reportRestore prints what a restore made and what to do next.
func reportRestore(w io.Writer, target string, res restored) {
	m := res.manifest
	_, _ = fmt.Fprintf(w, "restored into %s: %d files, %d directories, %d bytes\n", target, res.files, res.dirs, res.bytes)
	_, _ = fmt.Fprintf(w, "archive: Clowder %s, made %s, schema %s\n", m.Version, m.CreatedAt.Format("2006-01-02 15:04:05 MST"), m.Schema)
	_, _ = fmt.Fprintf(w, "counts: %d users, %d rooms, %d posts and comments, %d messages, %d files\n",
		m.Counts.Users, m.Counts.Rooms, m.Counts.Posts, m.Counts.Messages, m.Counts.Files)
	if m.Database == databaseExternal {
		_, _ = fmt.Fprintln(w, "the database is not in the archive: restore it first, into an empty PostgreSQL database,")
		_, _ = fmt.Fprintln(w, "with pg_restore (a pg_dump --format=custom file) or psql (a plain SQL dump)")
		_, _ = fmt.Fprintf(w, "then run: clowder serve -data %s -database-url <the URL of that database>\n", target)
		return
	}
	_, _ = fmt.Fprintf(w, "next: clowder serve -data %s\n", target)
}

const (
	tagRestoreInputInvalid     = "RestoreInputInvalid"
	tagRestoreTargetInvalid    = "RestoreTargetInvalid"
	tagRestoreTargetNotEmpty   = "RestoreTargetNotEmpty"
	tagRestoreArchiveInvalid   = "RestoreArchiveInvalid"
	tagRestoreArchiveTooLarge  = "RestoreArchiveTooLarge"
	tagRestoreManifestNotFirst = "RestoreManifestNotFirst"
	tagRestoreManifestInvalid  = "RestoreManifestInvalid"
	tagRestoreFormatUnknown    = "RestoreFormatUnknown"
	tagRestoreSchemaTooNew     = "RestoreSchemaTooNew"
	tagRestoreEntryUnsafe      = "RestoreEntryUnsafe"
	tagRestoreEntryUnlisted    = "RestoreEntryUnlisted"
	tagRestoreEntryDuplicate   = "RestoreEntryDuplicate"
	tagRestoreEntryMissing     = "RestoreEntryMissing"
	tagRestoreSizeMismatch     = "RestoreSizeMismatch"
	tagRestoreChecksumMismatch = "RestoreChecksumMismatch"
	tagRestoreWriteFailed      = "RestoreWriteFailed"
)
