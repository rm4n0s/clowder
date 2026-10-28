package main

import (
	"context"
	"io"
	"time"

	"github.com/rm4n0s/clowder/internal/config"
)

// The format of a backup archive (SPEC D59): a tar.gz whose entries are
// regular files and directories under one top directory. The first entry is
// the manifest, which lists every other entry with its size and SHA-256.
const (
	// backupTopDir is the top directory of every entry of the archive.
	backupTopDir = "clowder-backup"
	// backupManifestName is the name of the manifest inside the top directory.
	backupManifestName = "manifest.json"
	// backupFormat is the version of the archive format that this binary writes
	// and reads.
	backupFormat = 1
	// backupHostKeyName is the name of the SSH host key inside the data
	// directory and inside the archive.
	backupHostKeyName = config.DefaultSSHHostKeyFile
	// databaseExternal is the value of the manifest's "database" when the
	// database is not in the archive (PostgreSQL).
	databaseExternal = "external"
	// entryFile and entryDir are the types of an entry in the manifest.
	entryFile = "file"
	entryDir  = "dir"
	// backupFileMode and backupDirMode are the modes of the entries that a
	// restore makes: only the owner reads or writes anything of Clowder's.
	backupFileMode = 0o600
	backupDirMode  = 0o700
	// maxManifestBytes caps the manifest, which a restore reads into memory
	// before it has checked anything else.
	maxManifestBytes = 64 << 20
	// maxEntryBytes caps the size one entry may declare, so that a sum of
	// sizes cannot overflow.
	maxEntryBytes = 1 << 50
	// tarBlock is the size of a block of a tar archive.
	tarBlock = 512
	// archiveMargin is how many bytes beyond what the manifest accounts for
	// a restore lets the decompressed archive have (the end-of-archive
	// blocks and the padding of the last entry are inside it).
	archiveMargin = 64 << 10
)

// backupManifest is manifest.json: what the archive is and what it holds.
type backupManifest struct {
	// Format is the version of the archive format (backupFormat).
	Format int `json:"format"`
	// Version is the version of the Clowder binary that made the archive.
	Version string `json:"clowder_version"`
	// CreatedAt is when the backup began, in UTC.
	CreatedAt time.Time `json:"created_at"`
	// Dialect is "sqlite" or "postgres".
	Dialect string `json:"dialect"`
	// Database is the name of the database entry ("clowder.db"), or
	// "external" when the database is not in the archive.
	Database string `json:"database"`
	// Schema is the ID of the last migration the database had applied.
	Schema string `json:"schema"`
	// Counts are the sizes of the data.
	Counts backupCounts `json:"counts"`
	// Entries lists every entry of the archive except the manifest, in the
	// order of the archive.
	Entries []backupEntry `json:"entries"`
}

// backupCounts are the sizes recorded in the manifest.
type backupCounts struct {
	// Users, Rooms, Posts (posts and comments together), Messages are the
	// numbers of rows, read just after the copy of the database was made.
	Users    int64 `json:"users"`
	Rooms    int64 `json:"rooms"`
	Posts    int64 `json:"posts"`
	Messages int64 `json:"messages"`
	// Files is the number of regular files under files/ in the archive,
	// avatars included.
	Files int64 `json:"files"`
}

// backupEntry is one entry of the archive as the manifest lists it.
type backupEntry struct {
	// Path is relative to the top directory, written with "/".
	Path string `json:"path"`
	// Type is entryFile or entryDir.
	Type string `json:"type"`
	// Size is the size in bytes of a file.
	Size int64 `json:"size,omitempty"`
	// SHA256 is the hex SHA-256 of the content of a file.
	SHA256 string `json:"sha256,omitempty"`
}

// ctxReader is a reader that stops with the error of its context.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

// Read reads from the wrapped reader unless the context is done.
func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
