package app

import (
	"context"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/rm4n0s/clowder/internal/files"
	"github.com/rm4n0s/errors"
)

// stagedNoteHeader is the first line of the note of a staged upload; the
// number is the version of the format.
const stagedNoteHeader = "clowder-upload-1"

// stagedScopePosts is the scope of the uploads staged for posts and comments;
// a note of another scope (the messages) is not accepted for a post.
const stagedScopePosts = "posts"

// stagedNoteFields is the number of lines of a note: the header, the scope,
// the uploader, the time it was uploaded (Unix seconds), the size, the width,
// the height, the content type and the name of the file.
const stagedNoteFields = 9

// StagedLifetime is how long an upload may wait for a post: SweepStaged
// removes the ones older (SPEC §7.12).
const StagedLifetime = 24 * time.Hour

// StagedUpload is a file uploaded for a later request to attach: the way a
// client of the API (SPEC §7.12) uploads, because its upload and its post are
// two requests. The ID is the one value the client keeps.
type StagedUpload struct {
	// ID names the upload in StagedUploads. It is the key of the file: 64
	// random hex characters nobody can guess.
	ID string
	// Upload is the file as Posts.Upload returned it.
	Upload Upload
}

// UploadStaged is Upload for a client that attaches the file in a later
// request. Besides the file it keeps a note beside it that says who uploaded
// it and what Upload returned, so that StagedUploads can give the upload back
// to the same person and to nobody else. The note lies in the files directory,
// which the processes of a cluster share (SPEC §8.1). The permissions and the
// errors are those of Upload. A file that is never attached stays on disk
// with its note.
func (p *Posts) UploadStaged(ctx context.Context, actor Actor, name string, r io.Reader) (StagedUpload, error) {
	up, err := p.Upload(ctx, actor, name, r)
	if err != nil {
		return StagedUpload{}, err
	}
	note := strings.Join([]string{
		stagedNoteHeader,
		stagedScopePosts,
		strconv.FormatUint(actor.UserID, 10),
		strconv.FormatInt(p.clk.Now().Unix(), 10),
		strconv.FormatInt(up.Size, 10),
		strconv.Itoa(up.Width),
		strconv.Itoa(up.Height),
		up.ContentType,
		up.FileName,
	}, "\n")
	if err := p.files.PutNote(up.Key, []byte(note)); err != nil {
		// The file would never be attached: take it off the disk again.
		p.cleaner.remove(context.WithoutCancel(ctx), []string{up.Key})
		return StagedUpload{}, err
	}
	return StagedUpload{ID: up.Key, Upload: up}, nil
}

// StagedUploads turns the IDs that UploadStaged returned into the uploads that
// Create, Reply and Edit take. Only the person who uploaded a file gets it
// back: an ID that is unknown, that is not an ID, that another person
// uploaded, or whose note is damaged fails the whole call with
// TagInvalidInput and the message "Upload the files again." under
// FieldAttachments (the message does not say which of the cases it was). An
// actor with no ID fails with TagUnauthenticated. The call changes nothing;
// the post that takes the uploads checks the files (Create), and ForgetStaged
// ends the notes.
func (p *Posts) StagedUploads(ctx context.Context, actor Actor, ids []string) ([]Upload, error) {
	return stagedUploads(p.files, actor, ids)
}

// stagedUploads is StagedUploads of Posts and Messages: both keep their files
// in the same store and take the same staged uploads.
func stagedUploads(fs *files.Store, actor Actor, ids []string) ([]Upload, error) {
	if actor.UserID == 0 {
		return nil, errors.New(TagUnauthenticated, "uploads need a login", "reason", "none")
	}
	uploads := make([]Upload, 0, len(ids))
	bad := 0
	for _, id := range ids {
		data, err := fs.Note(id)
		if err != nil {
			if appErr, ok := errors.FromError(err); ok && (appErr.Tag == files.TagBadKey || appErr.Tag == files.TagNotFound) {
				bad++
				continue
			}
			return nil, err
		}
		up, ok := parseStagedNote(string(data), id, actor.UserID)
		if !ok {
			bad++
			continue
		}
		uploads = append(uploads, up)
	}
	if bad > 0 {
		return nil, errors.New(TagInvalidInput, "an upload is unknown or not the actor's",
			"fields", map[string]string{FieldAttachments: messageUploadAgain}, "userID", actor.UserID, "count", bad)
	}
	return uploads, nil
}

// ForgetStaged ends the notes of uploads that StagedUploads returned and that a
// post took or refused: the person has to upload a file again to attach it to
// another post. Failures are logged, never returned. An actor with no ID does
// nothing.
func (p *Posts) ForgetStaged(ctx context.Context, actor Actor, uploads []Upload) {
	forgetStaged(ctx, p.files, p.cleaner, actor, uploads)
}

// forgetStaged is ForgetStaged of Posts and Messages.
func forgetStaged(ctx context.Context, fs *files.Store, cleaner fileCleaner, actor Actor, uploads []Upload) {
	if actor.UserID == 0 {
		return
	}
	for _, u := range uploads {
		if err := fs.RemoveNote(u.Key); err != nil {
			cleaner.logger.ErrorContext(ctx, "removing the note of an upload failed", "err", err)
		}
	}
}

// stagedCreated reads when the note of a staged upload was written, whatever
// its scope (the messages keep the same notes). ok is false when the note is
// damaged.
func stagedCreated(note string) (time.Time, bool) {
	lines := strings.SplitN(note, "\n", stagedNoteFields)
	if len(lines) != stagedNoteFields || lines[0] != stagedNoteHeader {
		return time.Time{}, false
	}
	seconds, err := strconv.ParseInt(lines[3], 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	return time.Unix(seconds, 0), true
}

// SweepStaged removes the staged uploads that no post took and that are older
// than maxAge: the file and its note (SPEC §7.12). A person who uploads and
// never posts would otherwise fill the disk. The age is the time written in
// the note, by the clock of the service; a note that is damaged counts from
// when it was last written. The notes of every scope are swept, the messages'
// too. A note whose file an attachment uses is only ended, never the file.
// Several processes may run it at once on the shared directory: what another
// has removed already is skipped, not an error. A note or file that cannot be
// read or removed is logged and left for the next sweep. It returns the number
// of uploads removed, and fails with TagSweepCanceled when ctx is done, and
// with the error of the store when the notes cannot be listed.
func (p *Posts) SweepStaged(ctx context.Context, maxAge time.Duration) (int, error) {
	notes, err := p.files.Notes()
	if err != nil {
		return 0, err
	}
	cutoff := p.clk.Now().Add(-maxAge)
	removed := 0
	for _, n := range notes {
		if err := ctx.Err(); err != nil {
			return removed, errors.NewErr(TagSweepCanceled, err).SetMetadata("removed", removed)
		}
		data, err := p.files.Note(n.Key)
		if err != nil {
			if appErr, ok := errors.FromError(err); !ok || appErr.Tag != files.TagNotFound {
				p.cleaner.logger.ErrorContext(ctx, "reading the note of a staged upload failed", "err", err)
			}
			continue // gone already, or left for the next sweep
		}
		written, ok := stagedCreated(string(data))
		if !ok {
			written = n.ModTime
		}
		if written.After(cutoff) {
			continue
		}
		used, err := p.st.StorageKeysInUse(ctx, []string{n.Key})
		if err != nil {
			p.cleaner.logger.ErrorContext(ctx, "checking a staged upload failed", "err", err)
			continue
		}
		if len(used) == 0 {
			if err := p.files.Remove(n.Key); err != nil {
				p.cleaner.logger.ErrorContext(ctx, "removing a staged upload failed", "err", err)
				continue
			}
		}
		if err := p.files.RemoveNote(n.Key); err != nil {
			p.cleaner.logger.ErrorContext(ctx, "removing the note of a staged upload failed", "err", err)
			continue
		}
		if len(used) == 0 {
			removed++
		}
	}
	return removed, nil
}

// parseStagedNote reads the note of the upload with the key. ok is false when
// the note is damaged, is not of the posts, or belongs to another user.
func parseStagedNote(note, key string, userID uint64) (Upload, bool) {
	lines := strings.SplitN(note, "\n", stagedNoteFields)
	if len(lines) != stagedNoteFields || lines[0] != stagedNoteHeader || lines[1] != stagedScopePosts {
		return Upload{}, false
	}
	owner, err := strconv.ParseUint(lines[2], 10, 64)
	if err != nil || owner != userID {
		return Upload{}, false
	}
	size, err := strconv.ParseInt(lines[4], 10, 64)
	if err != nil || size < 0 {
		return Upload{}, false
	}
	width, err := strconv.Atoi(lines[5])
	if err != nil || width < 0 {
		return Upload{}, false
	}
	height, err := strconv.Atoi(lines[6])
	if err != nil || height < 0 {
		return Upload{}, false
	}
	return Upload{Key: key, FileName: lines[8], ContentType: lines[7], Size: size, Width: width, Height: height}, true
}

// TagSweepCanceled is the tag of a sweep of the staged uploads that ctx ended;
// the metadata "removed" is how many it had removed.
const TagSweepCanceled = "StagedSweepCanceled"
