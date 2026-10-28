package app

import (
	"context"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/rm4n0s/clowder/internal/config"
	"github.com/rm4n0s/clowder/internal/files"
	"github.com/rm4n0s/clowder/internal/live"
	"github.com/rm4n0s/clowder/internal/store"
	"github.com/rm4n0s/errors"
)

// FieldAttachments names the attachments of a post in the metadata "fields"
// of a TagInvalidInput error.
const FieldAttachments = "attachments"

// TagTooLarge is the tag of an upload larger than the limit (SPEC §7.5). It is
// the tag of the files package, the very same string: the error travels up
// from Posts.Upload untouched. The metadata "limit" is the limit in bytes. The
// handlers answer 413.
const TagTooLarge = files.TagTooLarge

// fileNameMaxLength is the longest file name that Upload keeps, in bytes.
const fileNameMaxLength = 255

// defaultFileName is the name of a file whose name is empty after cleaning.
const defaultFileName = "file"

// Messages of the field "attachments".
const (
	messageUploadAgain = "Upload the files again."
)

// Upload is a file that a person uploaded for a post and that is on disk but
// not yet on any post. Posts.Upload makes it; PostInput.Uploads takes it. Only
// the values that Upload returned are accepted: Create, Reply and Edit check
// that the file exists with that size and that no attachment uses it yet.
type Upload struct {
	// Key is the name of the file in the store: 64 lowercase hex characters.
	Key string
	// FileName is the cleaned name the file had when it was uploaded.
	FileName string
	// ContentType is the type found by sniffing the content.
	ContentType string
	// Size is the size in bytes.
	Size int64
	// Width and Height are the size in pixels of an image; 0 for any other
	// file.
	Width, Height int
}

// AttachmentView is an attachment as the services show it.
type AttachmentView struct {
	// ID is the ID of the attachment.
	ID uint64
	// FileName is the name the file had when it was uploaded.
	FileName string
	// ContentType is the type found by sniffing the content.
	ContentType string
	// Size is the size in bytes.
	Size int64
	// IsImage is true for the types shown inline: PNG, JPEG, GIF and WebP.
	IsImage bool
	// Width and Height are the size in pixels of an image; 0 for any other
	// file.
	Width, Height int
	// CreatedAt is when the file was attached. The download answers it as the
	// time the file was last modified.
	CreatedAt time.Time
}

// PostsConfig holds the settings of the posts service.
type PostsConfig struct {
	// MaxAttachmentSize is the largest file, in bytes, that Upload accepts;
	// 0 means the default (25 MB).
	MaxAttachmentSize int64
	// MaxAttachments is the most attachments one post may carry; 0 means the
	// default (10).
	MaxAttachments int
	// Logger receives the files that could not be removed from disk. Nil
	// means nothing is logged.
	Logger *slog.Logger
	// Presence tells who is online, for @here (SPEC §7.15). Nil means that
	// nobody is.
	Presence live.Presence
}

// fileCleaner removes files from disk when the database no longer refers to
// them. A file that cannot be removed is logged and left: it is not worth
// failing a change that is already committed.
type fileCleaner struct {
	files  *files.Store
	logger *slog.Logger
}

// newFileCleaner makes a fileCleaner; a nil logger discards.
func newFileCleaner(fs *files.Store, logger *slog.Logger) fileCleaner {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return fileCleaner{files: fs, logger: logger}
}

// remove removes the files under keys, best effort.
func (c fileCleaner) remove(ctx context.Context, keys []string) {
	for _, key := range keys {
		if err := c.files.Remove(key); err != nil {
			c.logger.ErrorContext(ctx, "removing a file failed", "err", err)
		}
	}
}

// discard removes the files of uploads that did not become attachments. A key
// that an attachment row uses is left alone: it belongs to that attachment.
func (c fileCleaner) discard(ctx context.Context, st *store.Store, uploads []Upload) {
	if len(uploads) == 0 {
		return
	}
	keys := make([]string, len(uploads))
	for i, u := range uploads {
		keys[i] = u.Key
	}
	// The context of the request may be the reason for the failure.
	ctx = context.WithoutCancel(ctx)
	used, err := st.StorageKeysInUse(ctx, keys)
	if err != nil {
		// Without the answer, removing could take a file from a post.
		c.logger.ErrorContext(ctx, "discarding the uploads failed", "err", err)
		return
	}
	inUse := make(map[string]bool, len(used))
	for _, k := range used {
		inUse[k] = true
	}
	var free []string
	for _, k := range keys {
		if !inUse[k] {
			free = append(free, k)
		}
	}
	c.remove(ctx, free)
}

// Upload stores a file that a person attached to the editor and returns what
// Create, Reply and Edit need to attach it (PostInput.Uploads). The file is
// at most the configured size; the name is cleaned (base name only, no control
// characters, at most 255 bytes, "file" when nothing is left). Any logged-in
// actor may upload; an actor with no ID fails with TagUnauthenticated, a file
// over the limit with TagTooLarge. An upload that no post takes is left on
// disk: callers pass every upload to the one call that makes the post, and
// that call removes the files when it fails.
func (p *Posts) Upload(ctx context.Context, actor Actor, name string, r io.Reader) (Upload, error) {
	if actor.UserID == 0 {
		return Upload{}, errors.New(TagUnauthenticated, "an upload needs a login", "reason", "none")
	}
	saved, err := p.files.Save(ctx, r, p.cfg.MaxAttachmentSize)
	if err != nil {
		return Upload{}, err
	}
	return Upload{
		Key:         saved.Key,
		FileName:    cleanFileName(name),
		ContentType: saved.ContentType,
		Size:        saved.Size,
		Width:       saved.Width,
		Height:      saved.Height,
	}, nil
}

// DiscardUploads removes the files of uploads that no post took, for a caller
// that made them with Upload and then failed before it could pass them to
// Create, Reply or Edit (which remove the files of their own failures). A file
// that an attachment already uses is left alone. An actor with no ID does
// nothing. Failures are logged, never returned.
func (p *Posts) DiscardUploads(ctx context.Context, actor Actor, uploads []Upload) {
	if actor.UserID == 0 {
		return
	}
	p.cleaner.discard(ctx, p.st, uploads)
}

// Attachment returns an attachment and its file for download. The room of its
// post must be readable by the actor: a missing attachment, one of a deleted
// post, and a post in a room that the actor may not read fail with
// TagNotFound. The caller closes the file.
func (p *Posts) Attachment(ctx context.Context, actor Actor, id uint64) (AttachmentView, io.ReadSeekCloser, error) {
	att, err := p.st.AttachmentByID(ctx, id)
	if err != nil {
		return AttachmentView{}, nil, err
	}
	post, err := p.st.PostByID(ctx, att.PostID)
	if err != nil {
		return AttachmentView{}, nil, err
	}
	if _, err := loadReadableRoom(ctx, p.st, actor, post.RoomID); err != nil {
		return AttachmentView{}, nil, err
	}
	if post.DeletedAt != nil {
		return AttachmentView{}, nil, errors.New(TagNotFound, "the post is deleted",
			"reason", "deleted", "postID", post.ID)
	}
	f, err := p.files.Open(att.StorageKey)
	if err != nil {
		return AttachmentView{}, nil, err
	}
	return attachmentView(att), f, nil
}

// attachmentView makes the view of an attachment row.
func attachmentView(a store.Attachment) AttachmentView {
	v := AttachmentView{
		ID:          a.ID,
		FileName:    a.FileName,
		ContentType: a.ContentType,
		Size:        a.Size,
		IsImage:     isInlineImage(a.ContentType),
		CreatedAt:   a.CreatedAt,
	}
	if a.Width != nil {
		v.Width = *a.Width
	}
	if a.Height != nil {
		v.Height = *a.Height
	}
	return v
}

// attachmentViews makes the views of rows; never nil.
func attachmentViews(rows []store.Attachment) []AttachmentView {
	views := make([]AttachmentView, len(rows))
	for i, a := range rows {
		views[i] = attachmentView(a)
	}
	return views
}

// isInlineImage reports whether a content type is one of the image types that
// are shown inline (SPEC §7.5).
func isInlineImage(contentType string) bool {
	base, _, _ := strings.Cut(contentType, ";")
	switch strings.ToLower(strings.TrimSpace(base)) {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return true
	}
	return false
}

// cleanFileName makes the name of an uploaded file safe to store and show: no
// invalid UTF-8, no control characters, only what follows the last "/" or
// "\", trimmed, at most 255 bytes (cut at a character), and "file" when
// nothing is left.
func cleanFileName(name string) string {
	name = strings.ToValidUTF8(name, "")
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, name)
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	name = strings.TrimSpace(name)
	if len(name) > fileNameMaxLength {
		cut := fileNameMaxLength
		for cut > 0 && !utf8.RuneStart(name[cut]) {
			cut--
		}
		name = strings.TrimSpace(name[:cut])
	}
	if name == "" || name == "." || name == ".." {
		return defaultFileName
	}
	return name
}

// withDefaults returns c with the defaults of SPEC §7.5 for what is not set.
func (c PostsConfig) withDefaults() PostsConfig {
	if c.MaxAttachmentSize <= 0 {
		c.MaxAttachmentSize = config.DefaultMaxAttachmentSize
	}
	if c.MaxAttachments <= 0 {
		c.MaxAttachments = config.DefaultMaxAttachmentsPerPost
	}
	return c
}

// checkUploads adds to fields, under FieldAttachments, the first problem of
// the uploads of a call; see checkUploadFiles.
func (p *Posts) checkUploads(ctx context.Context, tx *store.Store, fields map[string]string, uploads []Upload, kept int) error {
	return checkUploadFiles(ctx, tx, p.files, p.cfg.MaxAttachments, fields, uploads, kept)
}

// checkUploadFiles adds to fields, under FieldAttachments, the first problem
// of the uploads of a call: more attachments than limit (kept is the number of
// attachments the post or message keeps besides the uploads), a negative size,
// a key twice, a key with no file on disk (or a file of another size) and a
// key that an attachment already uses. It returns an error only when the
// database or the disk fail.
func checkUploadFiles(ctx context.Context, tx *store.Store, fs *files.Store, limit int, fields map[string]string, uploads []Upload, kept int) error {
	if kept+len(uploads) > limit {
		fields[FieldAttachments] = "Attach at most " + strconv.Itoa(limit) + " files."
	}
	if len(uploads) == 0 {
		return nil
	}
	keys := make([]string, 0, len(uploads))
	seen := make(map[string]bool, len(uploads))
	for _, u := range uploads {
		if u.Size < 0 || u.Width < 0 || u.Height < 0 || seen[u.Key] {
			addOnce(fields, FieldAttachments, messageUploadAgain)
			continue
		}
		seen[u.Key] = true
		keys = append(keys, u.Key)
		size, err := fs.Size(u.Key)
		if err != nil {
			if appErr, ok := errors.FromError(err); ok && (appErr.Tag == files.TagBadKey || appErr.Tag == files.TagNotFound) {
				addOnce(fields, FieldAttachments, messageUploadAgain)
				continue
			}
			return err
		}
		if size != u.Size {
			addOnce(fields, FieldAttachments, messageUploadAgain)
		}
	}
	used, err := tx.StorageKeysInUse(ctx, keys)
	if err != nil {
		return err
	}
	if len(used) > 0 {
		addOnce(fields, FieldAttachments, messageUploadAgain)
	}
	return nil
}

// addOnce sets fields[name] to message unless a message is already there.
func addOnce(fields map[string]string, name, message string) {
	if _, ok := fields[name]; !ok {
		fields[name] = message
	}
}

// attachmentRows makes the rows of the uploads of one post.
func attachmentRows(postID, uploaderID uint64, uploads []Upload) []store.Attachment {
	rows := make([]store.Attachment, len(uploads))
	for i, u := range uploads {
		row := store.Attachment{
			PostID:      postID,
			UploaderID:  uploaderID,
			FileName:    cleanFileName(u.FileName),
			ContentType: u.ContentType,
			Size:        u.Size,
			StorageKey:  u.Key,
		}
		if u.Width > 0 && u.Height > 0 {
			w, h := u.Width, u.Height
			row.Width, row.Height = &w, &h
		}
		rows[i] = row
	}
	return rows
}

// storageKeys returns the storage keys of the rows.
func storageKeys(rows []store.Attachment) []string {
	keys := make([]string, len(rows))
	for i, a := range rows {
		keys[i] = a.StorageKey
	}
	return keys
}
