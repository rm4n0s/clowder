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
	"log/slog"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	"github.com/rm4n0s/clowder/internal/clock"
	"github.com/rm4n0s/clowder/internal/config"
	"github.com/rm4n0s/clowder/internal/files"
	"github.com/rm4n0s/clowder/internal/store"
	"github.com/rm4n0s/errors"
)

// stdStream is the value of -o and -i that names the standard output or input.
const stdStream = "-"

// stagedBatch is how many staged keys one query asks about.
const stagedBatch = 500

// backupParams are the settings of one backup, checked.
type backupParams struct {
	// output is the resolved path of the archive, or stdStream.
	output string
	// dataDir is the data directory.
	dataDir string
	// databaseURL is the PostgreSQL URL, empty for SQLite.
	databaseURL string
}

// backupItem is one entry of the archive to be: its manifest entry and where
// its content is.
type backupItem struct {
	entry backupEntry
	// src is the path of the file on disk; empty for a directory.
	src string
	// modTime is the modification time recorded in the tar header.
	modTime time.Time
}

// runBackup writes the archive of the installation (SPEC D59): on SQLite a
// consistent copy of the database, the files directory and the SSH host key;
// on PostgreSQL the files directory and the host key, and the pg_dump command
// for the database. It works while serve runs. The archive is written to a
// temporary file next to -o and renamed, so a failed backup leaves nothing
// under the name of -o. With -o - the archive goes to stdout, after it was
// completed in a temporary file in the data directory, and the summary goes to
// stderr.
func runBackup(ctx context.Context, args []string, env func(string) string, stdout, stderr io.Writer) error {
	cfg := config.Default()
	var output string
	flags := flag.NewFlagSet("backup", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&output, "o", "", "the archive to write, a .tar.gz file; - writes it to the standard output")
	flags.StringVar(&cfg.DataDir, "data", envString(env, "CLOWDER_DATA", config.DefaultDataDir),
		"data directory (CLOWDER_DATA)")
	databaseFromEnv := urlFlag(flags, &cfg.DatabaseURL, "database-url", env, "CLOWDER_DATABASE_URL", cfg.DatabaseURL,
		"PostgreSQL URL, postgres://...; empty: SQLite in the data directory (CLOWDER_DATABASE_URL)")
	if err := flags.Parse(args); err != nil {
		return errors.NewErr(tagFlagsInvalid, err)
	}
	if flags.NArg() > 0 {
		return errors.New(tagFlagsInvalid, "unexpected arguments", "args", flags.Args())
	}
	databaseFromEnv()
	if output == "" {
		return errors.New(tagBackupOutputInvalid, "give the archive to write with -o (a file name, or - for the standard output)")
	}
	if err := cfg.Validate(); err != nil {
		return err
	}

	dataDir, err := filepath.Abs(cfg.DataDir)
	if err != nil {
		return errors.NewErr(tagBackupDataDirInvalid, err).SetMetadata("dir", cfg.DataDir)
	}
	info, err := os.Stat(dataDir)
	if err != nil {
		return errors.NewErr(tagBackupDataDirInvalid, err).SetMetadata("dir", dataDir)
	}
	if !info.IsDir() {
		return errors.New(tagBackupDataDirInvalid, "the data directory is not a directory", "dir", dataDir)
	}
	if cfg.DatabaseURL == "" {
		dbInfo, err := os.Stat(filepath.Join(dataDir, databaseFile))
		if err != nil {
			return errors.NewErr(tagBackupDataDirInvalid, err).SetMetadata("dir", dataDir, "reason", "no database file")
		}
		if !dbInfo.Mode().IsRegular() {
			return errors.New(tagBackupDataDirInvalid, "the database is not a regular file", "dir", dataDir)
		}
	}
	params := backupParams{output: stdStream, dataDir: dataDir, databaseURL: cfg.DatabaseURL}
	if output != stdStream {
		resolved, err := resolveBackupOutput(output, dataDir)
		if err != nil {
			return err
		}
		params.output = resolved
	}

	logger := newLogger(cfg.LogLevel, stderr)
	summary := stdout
	if params.output == stdStream {
		summary = stderr
	}
	return makeBackup(ctx, params, logger, stdout, summary)
}

// resolveBackupOutput checks the path of the archive and returns it absolute,
// with the symbolic links of its directory resolved. It refuses a path inside
// the files directory (the archive would back itself up), the database file,
// the host key, an existing directory, and a directory that does not exist.
func resolveBackupOutput(output, dataDir string) (string, error) {
	abs, err := filepath.Abs(output)
	if err != nil {
		return "", errors.NewErr(tagBackupOutputInvalid, err).SetMetadata("path", output)
	}
	dir, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return "", errors.NewErr(tagBackupOutputInvalid, err).SetMetadata("path", output, "reason", "directory")
	}
	resolved := filepath.Join(dir, filepath.Base(abs))
	filesRoot := filepath.Join(dataDir, filesDir)
	if real, err := filepath.EvalSymlinks(filesRoot); err == nil {
		filesRoot = real
	}
	if resolved == filesRoot || strings.HasPrefix(resolved, filesRoot+string(filepath.Separator)) {
		return "", errors.New(tagBackupOutputInvalid, "the archive must not be written inside the files directory",
			"path", output)
	}
	info, err := os.Stat(resolved)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return resolved, nil
	case err != nil:
		return "", errors.NewErr(tagBackupOutputInvalid, err).SetMetadata("path", output, "reason", "stat")
	case info.IsDir():
		return "", errors.New(tagBackupOutputInvalid, "the archive path is a directory", "path", output)
	}
	// An existing file is replaced, except the ones the installation keeps.
	for _, name := range []string{databaseFile, backupHostKeyName} {
		other, err := os.Stat(filepath.Join(dataDir, name))
		if err == nil && os.SameFile(info, other) {
			return "", errors.New(tagBackupOutputInvalid, "the archive path is a file of the installation",
				"path", output, "file", name)
		}
	}
	return resolved, nil
}

// makeBackup does the work of runBackup. archive receives the archive when
// p.output is stdStream; summary receives the report.
func makeBackup(ctx context.Context, p backupParams, logger *slog.Logger, archive, summary io.Writer) error {
	st, err := openStore(ctx, p.dataDir, p.databaseURL, clock.Real(), logger)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := st.Close(); closeErr != nil {
			logger.Error("closing the store failed", "err", closeErr)
		}
	}()

	m := backupManifest{
		Format:    backupFormat,
		Version:   buildVersion(),
		CreatedAt: clock.Real().Now().UTC(),
		Dialect:   "sqlite",
		Database:  databaseFile,
	}
	if st.IsPostgres() {
		m.Dialect, m.Database = "postgres", databaseExternal
	}
	if m.Schema, err = st.SchemaVersion(ctx); err != nil {
		return err
	}

	var items []backupItem
	if !st.IsPostgres() {
		// The copy of the database comes first and the files after it, so that
		// every attachment the copy names is in the archive.
		tmpDB, err := os.CreateTemp(p.dataDir, "backup-*.db")
		if err != nil {
			return errors.NewErr(tagBackupTempFailed, err).SetMetadata("dir", p.dataDir, "what", "database copy")
		}
		dbCopy := tmpDB.Name()
		defer func() {
			if rmErr := os.Remove(dbCopy); rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) {
				logger.Error("removing the database copy failed", "err", rmErr, "path", dbCopy)
			}
		}()
		if err := tmpDB.Close(); err != nil {
			return errors.NewErr(tagBackupTempFailed, err).SetMetadata("dir", p.dataDir, "what", "database copy")
		}
		if err := st.VacuumInto(ctx, dbCopy); err != nil {
			return err
		}
		items = append(items, backupItem{
			entry:   backupEntry{Path: databaseFile, Type: entryFile},
			src:     dbCopy,
			modTime: m.CreatedAt,
		})
	}
	counts, err := st.BackupCounts(ctx)
	if err != nil {
		return err
	}
	m.Counts = backupCounts{Users: counts.Users, Rooms: counts.Rooms, Posts: counts.Posts, Messages: counts.Messages}

	hostKey := filepath.Join(p.dataDir, backupHostKeyName)
	keyInfo, err := os.Stat(hostKey)
	switch {
	case err == nil && keyInfo.Mode().IsRegular():
		items = append(items, backupItem{
			entry:   backupEntry{Path: backupHostKeyName, Type: entryFile},
			src:     hostKey,
			modTime: keyInfo.ModTime().UTC(),
		})
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		return errors.NewErr(tagBackupFilesFailed, err).SetMetadata("path", hostKey)
	}

	staged, err := stagedUploads(ctx, st, filepath.Join(p.dataDir, filesDir))
	if err != nil {
		return err
	}
	fileItems, err := collectFiles(ctx, filepath.Join(p.dataDir, filesDir), staged, logger)
	if err != nil {
		return err
	}
	items = append(items, fileItems...)
	for i := range items {
		if items[i].entry.Type != entryFile {
			continue
		}
		if err := hashItem(ctx, &items[i]); err != nil {
			return err
		}
		if strings.HasPrefix(items[i].entry.Path, filesDir+"/") {
			m.Counts.Files++
		}
	}
	for _, it := range items {
		m.Entries = append(m.Entries, it.entry)
	}

	outDir := p.dataDir
	if p.output != stdStream {
		outDir = filepath.Dir(p.output)
	}
	tmp, err := os.CreateTemp(outDir, ".clowder-backup-*.tmp")
	if err != nil {
		return errors.NewErr(tagBackupTempFailed, err).SetMetadata("dir", outDir, "what", "archive")
	}
	tmpName := tmp.Name()
	renamed := false
	defer func() {
		if renamed {
			return
		}
		_ = tmp.Close()
		if rmErr := os.Remove(tmpName); rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) {
			logger.Error("removing the unfinished archive failed", "err", rmErr, "path", tmpName)
		}
	}()
	if err := writeBackupArchive(ctx, tmp, m, items); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return errors.NewErr(tagBackupWriteFailed, err).SetMetadata("path", tmpName, "step", "sync")
	}
	size, err := tmp.Seek(0, io.SeekEnd)
	if err != nil {
		return errors.NewErr(tagBackupWriteFailed, err).SetMetadata("path", tmpName, "step", "size")
	}
	if p.output == stdStream {
		if _, err := tmp.Seek(0, io.SeekStart); err != nil {
			return errors.NewErr(tagBackupWriteFailed, err).SetMetadata("path", tmpName, "step", "rewind")
		}
		if _, err := io.Copy(archive, ctxReader{ctx: ctx, r: tmp}); err != nil {
			return errors.NewErr(tagBackupWriteFailed, err).SetMetadata("step", "stdout")
		}
	} else {
		if err := tmp.Close(); err != nil {
			return errors.NewErr(tagBackupWriteFailed, err).SetMetadata("path", tmpName, "step", "close")
		}
		if err := os.Rename(tmpName, p.output); err != nil {
			return errors.NewErr(tagBackupWriteFailed, err).SetMetadata("path", p.output, "step", "rename")
		}
		renamed = true
	}
	reportBackup(summary, p, m, size)
	return nil
}

// stagedUploads returns the keys of the uploads that were staged and not
// attached to anything: a file in the files directory that has a note and that
// no attachment row uses. They are the uploads that nobody posted and that
// the server removes after a day, and a backup leaves them out.
func stagedUploads(ctx context.Context, st *store.Store, filesRoot string) (map[string]bool, error) {
	notesDir := filepath.Join(filesRoot, files.NotesDir)
	entries, err := os.ReadDir(notesDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, errors.NewErr(tagBackupFilesFailed, err).SetMetadata("dir", notesDir)
	}
	var keys []string
	for _, e := range entries {
		if files.IsKey(e.Name()) {
			keys = append(keys, e.Name())
		}
	}
	staged := make(map[string]bool, len(keys))
	for _, k := range keys {
		staged[k] = true
	}
	for start := 0; start < len(keys); start += stagedBatch {
		if err := ctx.Err(); err != nil {
			return nil, errors.NewErr(tagBackupFilesFailed, err).SetMetadata("dir", notesDir)
		}
		used, err := st.StorageKeysInUse(ctx, keys[start:min(start+stagedBatch, len(keys))])
		if err != nil {
			return nil, err
		}
		// A note is removed after the post that took the upload was
		// committed: the file may still have its note and be in use.
		for _, k := range used {
			delete(staged, k)
		}
	}
	return staged, nil
}

// collectFiles lists the files directory for the archive: the directory
// itself, and everything under it that is a directory or a regular file,
// except the directory of the files being written, the notes of staged
// uploads and the staged files. Links and other special files are skipped
// with a warning. A files directory that does not exist is an empty one.
func collectFiles(ctx context.Context, filesRoot string, staged map[string]bool, logger *slog.Logger) ([]backupItem, error) {
	items := []backupItem{{entry: backupEntry{Path: filesDir, Type: entryDir}, modTime: time.Now().UTC()}}
	root, err := filepath.EvalSymlinks(filesRoot)
	if errors.Is(err, fs.ErrNotExist) {
		return items, nil
	}
	if err != nil {
		return nil, errors.NewErr(tagBackupFilesFailed, err).SetMetadata("dir", filesRoot)
	}
	skipDirs := map[string]bool{
		files.TmpDir:                        true,
		files.NotesDir:                      true,
		path.Join(avatarsDir, files.TmpDir): true,
	}
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, fs.ErrNotExist) && p != root {
				return nil // removed while the walk ran
			}
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if p == root {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		switch {
		case d.IsDir():
			if skipDirs[rel] {
				return filepath.SkipDir
			}
			info, err := d.Info()
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			if err != nil {
				return err
			}
			items = append(items, backupItem{
				entry:   backupEntry{Path: path.Join(filesDir, rel), Type: entryDir},
				modTime: info.ModTime().UTC(),
			})
		case d.Type().IsRegular():
			if staged[rel] {
				return nil
			}
			info, err := d.Info()
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			if err != nil {
				return err
			}
			items = append(items, backupItem{
				entry:   backupEntry{Path: path.Join(filesDir, rel), Type: entryFile},
				src:     p,
				modTime: info.ModTime().UTC(),
			})
		default:
			logger.Warn("a backup leaves out a file that is not a regular file", "path", p)
		}
		return nil
	})
	if err != nil {
		return nil, errors.NewErr(tagBackupFilesFailed, err).SetMetadata("dir", filesRoot)
	}
	return items, nil
}

// hashItem reads the file of it and records its size and SHA-256.
func hashItem(ctx context.Context, it *backupItem) error {
	f, err := os.Open(it.src)
	if err != nil {
		return errors.NewErr(tagBackupFilesFailed, err).SetMetadata("path", it.entry.Path)
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	n, err := io.Copy(h, ctxReader{ctx: ctx, r: f})
	if err != nil {
		return errors.NewErr(tagBackupFilesFailed, err).SetMetadata("path", it.entry.Path)
	}
	it.entry.Size = n
	it.entry.SHA256 = hex.EncodeToString(h.Sum(nil))
	return nil
}

// writeBackupArchive writes the tar.gz to w: the manifest first, then the
// items in order. Every file is read again and checked against the size and
// the SHA-256 that the manifest says, so an archive never holds a file that
// differs from its manifest.
func writeBackupArchive(ctx context.Context, w io.Writer, m backupManifest, items []backupItem) error {
	manifest, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return errors.NewErr(tagBackupWriteFailed, err).SetMetadata("step", "manifest")
	}
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	head := &tar.Header{
		Typeflag: tar.TypeReg,
		Name:     backupTopDir + "/" + backupManifestName,
		Mode:     backupFileMode,
		Size:     int64(len(manifest)),
		ModTime:  m.CreatedAt,
	}
	if err := tw.WriteHeader(head); err != nil {
		return errors.NewErr(tagBackupWriteFailed, err).SetMetadata("step", "manifest")
	}
	if _, err := tw.Write(manifest); err != nil {
		return errors.NewErr(tagBackupWriteFailed, err).SetMetadata("step", "manifest")
	}
	for _, it := range items {
		if err := ctx.Err(); err != nil {
			return errors.NewErr(tagBackupWriteFailed, err).SetMetadata("path", it.entry.Path)
		}
		if it.entry.Type == entryDir {
			dir := &tar.Header{
				Typeflag: tar.TypeDir,
				Name:     backupTopDir + "/" + it.entry.Path + "/",
				Mode:     backupDirMode,
				ModTime:  it.modTime,
			}
			if err := tw.WriteHeader(dir); err != nil {
				return errors.NewErr(tagBackupWriteFailed, err).SetMetadata("path", it.entry.Path)
			}
			continue
		}
		file := &tar.Header{
			Typeflag: tar.TypeReg,
			Name:     backupTopDir + "/" + it.entry.Path,
			Mode:     backupFileMode,
			Size:     it.entry.Size,
			ModTime:  it.modTime,
		}
		if err := tw.WriteHeader(file); err != nil {
			return errors.NewErr(tagBackupWriteFailed, err).SetMetadata("path", it.entry.Path)
		}
		if err := copyBackupFile(ctx, tw, it); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return errors.NewErr(tagBackupWriteFailed, err).SetMetadata("step", "tar")
	}
	if err := gz.Close(); err != nil {
		return errors.NewErr(tagBackupWriteFailed, err).SetMetadata("step", "gzip")
	}
	return nil
}

// copyBackupFile copies the content of it to tw, and fails with
// BackupFileChanged when what it read is not what the manifest says.
func copyBackupFile(ctx context.Context, tw *tar.Writer, it backupItem) error {
	f, err := os.Open(it.src)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return errors.NewErr(tagBackupFileChanged, err).SetMetadata("path", it.entry.Path)
		}
		return errors.NewErr(tagBackupFilesFailed, err).SetMetadata("path", it.entry.Path)
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	n, err := io.CopyN(io.MultiWriter(tw, h), ctxReader{ctx: ctx, r: f}, it.entry.Size)
	if err != nil {
		if errors.Is(err, io.EOF) {
			return errors.New(tagBackupFileChanged, "the file became shorter while it was copied",
				"path", it.entry.Path, "size", it.entry.Size, "read", n)
		}
		return errors.NewErr(tagBackupWriteFailed, err).SetMetadata("path", it.entry.Path)
	}
	if hex.EncodeToString(h.Sum(nil)) != it.entry.SHA256 {
		return errors.New(tagBackupFileChanged, "the file changed while it was copied", "path", it.entry.Path)
	}
	return nil
}

// reportBackup prints what a backup made.
func reportBackup(w io.Writer, p backupParams, m backupManifest, size int64) {
	where := p.output
	if where == stdStream {
		where = "the standard output"
	}
	_, _ = fmt.Fprintf(w, "backup written: %s (%d bytes)\n", where, size)
	if m.Database == databaseExternal {
		_, _ = fmt.Fprintln(w, "database: PostgreSQL, not in the archive")
	} else {
		_, _ = fmt.Fprintf(w, "database: SQLite, a consistent copy (schema %s)\n", m.Schema)
	}
	_, _ = fmt.Fprintf(w, "counts: %d users, %d rooms, %d posts and comments, %d messages, %d files\n",
		m.Counts.Users, m.Counts.Rooms, m.Counts.Posts, m.Counts.Messages, m.Counts.Files)
	if m.Database == databaseExternal {
		cmd, passwordLeftOut := pgDumpCommand(p.databaseURL)
		_, _ = fmt.Fprintln(w, "save the database with pg_dump; run it BEFORE clowder backup (a dump made after the archive can name attachments that the archive lacks):")
		_, _ = fmt.Fprintf(w, "  %s\n", cmd)
		if passwordLeftOut {
			_, _ = fmt.Fprintln(w, "  (the password is left out: give it to pg_dump with PGPASSWORD or ~/.pgpass)")
		}
		_, _ = fmt.Fprintln(w, "restore it with pg_restore into an empty database before you start the server on a restored archive")
	}
}

// pgDumpCommand returns the pg_dump command that saves the database at
// databaseURL, with the URL's password left out, and whether it had one.
func pgDumpCommand(databaseURL string) (cmd string, passwordLeftOut bool) {
	const prefix = "pg_dump --format=custom --file=clowder.pgdump "
	u, err := url.Parse(databaseURL)
	if err != nil {
		return prefix + "'<database URL>'", false
	}
	if u.User != nil {
		if _, ok := u.User.Password(); ok {
			passwordLeftOut = true
		}
		u.User = url.User(u.User.Username())
	}
	if u.RawQuery != "" {
		q := u.Query()
		for _, key := range []string{"password", "sslpassword"} {
			if q.Has(key) {
				passwordLeftOut = true
				q.Del(key)
			}
		}
		u.RawQuery = q.Encode()
	}
	return prefix + "'" + strings.ReplaceAll(u.String(), "'", `'\''`) + "'", passwordLeftOut
}

// buildVersion returns the version of the module that was built, or "devel"
// for a build from a checkout.
func buildVersion() string {
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "devel"
}

const (
	tagBackupOutputInvalid  = "BackupOutputInvalid"
	tagBackupDataDirInvalid = "BackupDataDirInvalid"
	tagBackupTempFailed     = "BackupTempFailed"
	tagBackupFilesFailed    = "BackupFilesFailed"
	tagBackupFileChanged    = "BackupFileChanged"
	tagBackupWriteFailed    = "BackupWriteFailed"
)
