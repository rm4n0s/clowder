package web

import (
	"context"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/errors"
)

// formFieldFiles is the name of the file input of the editor.
const formFieldFiles = "files"

// requestBodySlack is what the cap of an upload request allows besides the
// files: the text fields and the multipart framing.
const requestBodySlack = 1 << 20

// requestBodyLimit is the cap on the body of an upload request: the most files
// of the most bytes, and a megabyte for the rest.
func (s *Server) requestBodyLimit() int64 {
	return int64(s.cfg.MaxAttachmentsPerPost)*s.cfg.MaxAttachmentSize + requestBodySlack
}

// isMultipart reports whether the request is sent as multipart/form-data.
func isMultipart(r *http.Request) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return err == nil && mediaType == "multipart/form-data"
}

// readEditorForm reads the form of an editor into r.PostForm and r.Form, so
// that r.PostFormValue and r.FormValue see the fields, and returns the files of
// the form, saved through Posts.Upload.
//
// A urlencoded form has no files and is read like parseBodyForm. A multipart
// form is streamed: the text fields (at most maxPostFormBytes together) are
// kept in memory, each file goes to disk while it is read, and the whole body
// is capped at requestBodyLimit. With keepFiles false the file parts are read
// and dropped (the preview does not need them).
//
// ok is false when the form cannot be read at all; then it has answered (the
// 400 page, or the error page for a service that fails). A problem with the
// files, such as one that is too large or too many of them, is returned as
// problem (TagTooLarge or TagInvalidInput) with the text fields read and
// uploads nil: the files that were saved have been removed. The caller shows
// the problem in the editor.
func (s *Server) readEditorForm(w http.ResponseWriter, r *http.Request, keepFiles bool) (uploads []app.Upload, ok bool, problem error) {
	return s.readUploadForm(w, r, keepFiles, s.postUploader())
}

// uploader stores the files of a form and removes them again: the service of
// the posts for the editor, the service of the messages for the composer.
type uploader struct {
	// upload saves one file and returns what the service needs to attach it.
	upload func(ctx context.Context, actor app.Actor, name string, r io.Reader) (app.Upload, error)
	// discard removes the files of uploads that no post or message took.
	discard func(ctx context.Context, actor app.Actor, uploads []app.Upload)
}

// postUploader is the uploader of the editor of posts.
func (s *Server) postUploader() uploader {
	return uploader{upload: s.svc.Posts.Upload, discard: s.svc.Posts.DiscardUploads}
}

// messageUploader is the uploader of the composer of messages.
func (s *Server) messageUploader() uploader {
	return uploader{upload: s.svc.Messages.Upload, discard: s.svc.Messages.DiscardUploads}
}

// readUploadForm is readEditorForm with the service that keeps the files given:
// the composer of the messages reads its form the same way.
func (s *Server) readUploadForm(w http.ResponseWriter, r *http.Request, keepFiles bool, up uploader) (uploads []app.Upload, ok bool, problem error) {
	if !isMultipart(r) {
		return nil, s.parseBodyForm(w, r), nil
	}
	actor, _ := actorFrom(r.Context())
	r.Body = http.MaxBytesReader(w, r.Body, s.requestBodyLimit())
	if err := r.ParseForm(); err != nil {
		s.rejectForm(w, r, errors.NewErr(tagFormInvalid, err))
		return nil, false, nil
	}
	mr, err := r.MultipartReader()
	if err != nil {
		s.rejectForm(w, r, errors.NewErr(tagFormInvalid, err))
		return nil, false, nil
	}
	values, saved, problem, fatal := s.readParts(r, actor, mr, keepFiles, up)
	for name, vs := range values {
		r.PostForm[name] = append(r.PostForm[name], vs...)
		r.Form[name] = append(r.Form[name], vs...)
	}
	if fatal != nil || problem != nil {
		up.discard(r.Context(), actor, saved)
	}
	switch {
	case fatal == nil:
	case isTag(fatal, tagFormInvalid):
		appErr, _ := errors.FromError(fatal)
		s.rejectForm(w, r, appErr)
		return nil, false, nil
	default:
		s.fail(w, r, fatal)
		return nil, false, nil
	}
	if problem != nil {
		return nil, true, problem
	}
	return saved, true, nil
}

// readParts walks the parts of a multipart form. It returns the text fields,
// the saved uploads, the problem with the files (nil if none) and the error of
// a form that cannot be read (a field that is too large, a broken body, a
// failing disk). After a problem with the files it goes on reading the text
// fields and drops the other files.
func (s *Server) readParts(r *http.Request, actor app.Actor, mr *multipart.Reader, keepFiles bool, up uploader) (values url.Values, saved []app.Upload, problem, fatal error) {
	values = url.Values{}
	budget := int64(maxPostFormBytes)
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			return values, saved, problem, nil
		}
		if err != nil {
			if isBodyTooLarge(err) {
				return values, saved, errors.New(app.TagTooLarge, "the upload is too large", "limit", s.cfg.MaxAttachmentSize), nil
			}
			return values, saved, problem, errors.NewErr(tagFormInvalid, err)
		}
		name := part.FormName()
		if name != formFieldFiles {
			data, err := io.ReadAll(io.LimitReader(part, budget+1))
			if err != nil {
				if isBodyTooLarge(err) {
					return values, saved, errors.New(app.TagTooLarge, "the upload is too large", "limit", s.cfg.MaxAttachmentSize), nil
				}
				return values, saved, problem, errors.NewErr(tagFormInvalid, err).SetMetadata("field", name)
			}
			if int64(len(data)) > budget {
				return values, saved, problem, errors.New(tagFormInvalid, "the text fields are too large", "field", name)
			}
			budget -= int64(len(data))
			values.Add(name, string(data))
			continue
		}
		// A browser sends an empty part, with no file name, for a file input
		// that has no file.
		if part.FileName() == "" || problem != nil || !keepFiles {
			continue
		}
		if len(saved) >= s.cfg.MaxAttachmentsPerPost {
			problem = errors.New(app.TagInvalidInput, "too many files",
				"fields", map[string]string{app.FieldAttachments: "Attach at most " + strconv.Itoa(s.cfg.MaxAttachmentsPerPost) + " files."})
			continue
		}
		saved1, err := up.upload(r.Context(), actor, part.FileName(), part)
		switch {
		case err == nil:
			saved = append(saved, saved1)
		case isTag(err, app.TagTooLarge):
			problem = err
		case isBodyTooLarge(err):
			problem = errors.New(app.TagTooLarge, "the upload is too large", "limit", s.cfg.MaxAttachmentSize)
		default:
			return values, saved, problem, err
		}
	}
}

// isBodyTooLarge reports whether err comes from http.MaxBytesReader.
func isBodyTooLarge(err error) bool {
	var tooLarge *http.MaxBytesError
	return errors.As(err, &tooLarge)
}

// keepOf reads the IDs of the attachments of a post that the edit keeps: the
// values "keep" without the values "remove". A value that is not an ID is 0,
// which the service refuses as an attachment the post does not have.
func keepOf(r *http.Request) []uint64 {
	removed := map[uint64]bool{}
	for _, v := range r.PostForm["remove"] {
		removed[idOfText(v)] = true
	}
	var keep []uint64
	for _, v := range r.PostForm["keep"] {
		if id := idOfText(v); !removed[id] {
			keep = append(keep, id)
		}
	}
	return keep
}

// idOfText reads an ID; 0 when the text is not one.
func idOfText(s string) uint64 {
	id, err := strconv.ParseUint(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0
	}
	return id
}
