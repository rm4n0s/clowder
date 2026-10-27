package store

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/rm4n0s/clowder/internal/clock"
	"github.com/rm4n0s/errors"
)

// templateFileMode is the permission of the database files that
// Template.Open writes.
const templateFileMode = 0o600

// Template is the image of an empty database whose schema is up to date. Making
// the schema (GORM's AutoMigrate, the raw migrations) costs tens of
// milliseconds, which adds up in a test suite that opens a store per test:
// NewTemplate pays it once and Open then gives each caller a copy of the file
// to open, without migrating. It is for tests (see package storetest); the
// program opens its database with Open. A Template is read-only after
// NewTemplate, so it is safe for concurrent use.
type Template struct {
	image []byte
}

// NewTemplate migrates a scratch database in a temporary directory, with the
// models and migrations of Open, and keeps the bytes of the file. The
// directory is removed before it returns. It fails with the tags of Open when
// the migration fails, with TemplateDirFailed when the directory cannot be
// made and with TemplateReadFailed when the file cannot be read.
func NewTemplate(ctx context.Context) (*Template, error) {
	dir, err := os.MkdirTemp("", "clowder-template-*")
	if err != nil {
		return nil, errors.NewErr(tagTemplateDirFailed, err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	return templateAt(ctx, filepath.Join(dir, "template.db"))
}

// templateAt migrates a database in the file path, which it closes, and keeps
// the bytes of the file. path may be a SQLite "file:" URI, which the tests use
// to make the read fail after the migration worked.
func templateAt(ctx context.Context, path string) (*Template, error) {
	s, err := Open(ctx, path, clock.Real(), nil)
	if err != nil {
		return nil, err
	}
	// Close checkpoints the write-ahead log into the main file.
	if err := s.Close(); err != nil {
		return nil, err
	}
	image, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.NewErr(tagTemplateReadFailed, err).SetMetadata("path", path)
	}
	return &Template{image: image}, nil
}

// Open writes a copy of the template to the file path, which must not be in
// use, and opens it like Open does, but without migrating: the copy is
// current. The time of every row comes from clk. A nil logger discards the log.
// It fails with TemplateWriteFailed when the file cannot be written and with
// the tags of Open when the copy cannot be opened.
func (t *Template) Open(ctx context.Context, path string, clk clock.Clock, logger *slog.Logger) (*Store, error) {
	if err := os.WriteFile(path, t.image, templateFileMode); err != nil {
		return nil, errors.NewErr(tagTemplateWriteFailed, err).SetMetadata("path", path)
	}
	// No models and no raw migrations: only the table that records them is
	// checked.
	return open(ctx, path, clk, logger, options{slowQuery: slowQueryThreshold})
}

const (
	tagTemplateDirFailed   = "TemplateDirFailed"
	tagTemplateReadFailed  = "TemplateReadFailed"
	tagTemplateWriteFailed = "TemplateWriteFailed"
)
