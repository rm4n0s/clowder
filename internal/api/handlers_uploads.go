package api

import (
	"io"
	"mime"
	"net/http"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/files"
	"github.com/rm4n0s/errors"
)

const (
	// uploadField is the name of the field of the form that holds the file.
	uploadField = "file"
	// uploadBodySlack is what the cap of an upload request allows besides the
	// file: the framing of the form.
	uploadBodySlack = 1 << 20
	// messageUploadOneFile is the problem with a form that is not one file in
	// the field "file".
	messageUploadOneFile = "Send exactly one file in the field file."
)

// uploadRoutes are the routes of the uploads and of the downloads of
// attachments.
func (s *Server) uploadRoutes() []Route {
	return []Route{
		s.route(http.MethodPost, "/uploads", false, s.upload),
		s.route(http.MethodGet, "/attachments/{id}", false, s.attachment),
	}
}

// upload answers POST /uploads: one file in the field "file" of a
// multipart/form-data body is stored, and its ID answers the request; a post
// or comment that is written or edited next takes it in upload_ids. See
// readUpload for what the form may hold.
func (s *Server) upload(w http.ResponseWriter, r *http.Request) {
	c, _ := callerFrom(r.Context())
	staged, err := s.readUpload(w, r, c.actor)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusCreated, uploadOf(staged))
}

// readUpload reads the form of an upload request and stores its file for the
// actor. The Content-Type must be multipart/form-data (415 otherwise). The
// body is capped before it is read, the file is streamed to disk and refused
// over the limit of the attachments (app.TagTooLarge), and the form must hold
// nothing but the file (tagUploadInvalid; a file stored before the rest was
// found wrong is removed again). The upload belongs to the actor: nobody else
// can attach it.
func (s *Server) readUpload(w http.ResponseWriter, r *http.Request, actor app.Actor) (app.StagedUpload, error) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" {
		return app.StagedUpload{}, errors.New(tagUnsupportedMediaType, "the body is not multipart/form-data",
			"contentType", r.Header.Get("Content-Type"))
	}
	r.Body = http.MaxBytesReader(w, r.Body, s.maxUploadBody)
	mr, err := r.MultipartReader()
	if err != nil {
		return app.StagedUpload{}, errors.NewErr(tagUploadInvalid, err).
			SetMetadata("fields", map[string]string{uploadField: messageUploadOneFile})
	}
	part, err := mr.NextPart()
	if err != nil {
		if isBodyTooLarge(err) {
			return app.StagedUpload{}, errors.NewErr(tagBodyTooLarge, err).SetMetadata("limit", s.maxUploadBody)
		}
		return app.StagedUpload{}, errors.NewErr(tagUploadInvalid, err).
			SetMetadata("fields", map[string]string{uploadField: messageUploadOneFile})
	}
	if part.FormName() != uploadField || part.FileName() == "" {
		return app.StagedUpload{}, errors.New(tagUploadInvalid, "the first part of the form is not the file",
			"fields", map[string]string{uploadField: messageUploadOneFile})
	}
	staged, err := s.svc.Posts.UploadStaged(r.Context(), actor, part.FileName(), part)
	if err != nil {
		switch {
		case isBodyTooLarge(err):
			return app.StagedUpload{}, errors.New(app.TagTooLarge, "the upload is too large", "limit", s.maxUploadBody)
		case errors.Is(err, io.ErrUnexpectedEOF):
			// The client's body ended inside the file: its mistake, not ours.
			return app.StagedUpload{}, errors.New(tagUploadInvalid, "the body ended inside the file",
				"fields", map[string]string{uploadField: messageUploadOneFile})
		}
		return app.StagedUpload{}, err
	}
	// Nothing may follow the file. Reading on also reads the end of the body.
	if _, err := mr.NextPart(); err != io.EOF {
		s.svc.Posts.DiscardUploads(r.Context(), actor, []app.Upload{staged.Upload})
		s.svc.Posts.ForgetStaged(r.Context(), actor, []app.Upload{staged.Upload})
		return app.StagedUpload{}, errors.New(tagUploadInvalid, "the form holds more than the file",
			"fields", map[string]string{uploadField: messageUploadOneFile})
	}
	return staged, nil
}

// isBodyTooLarge reports whether err comes from http.MaxBytesReader.
func isBodyTooLarge(err error) bool {
	var tooLarge *http.MaxBytesError
	return errors.As(err, &tooLarge)
}

// attachment answers GET /attachments/{id} with the file of an attachment of a
// post or comment in a room the caller may read: the same checks and headers
// as the pages' download (files.ServeDownload). Anyone else, a missing
// attachment and one of a deleted post get 404. Only the image types of SPEC
// §7.5 are served inline with their own type; every other file is
// application/octet-stream with Content-Disposition attachment.
func (s *Server) attachment(w http.ResponseWriter, r *http.Request) {
	c, _ := callerFrom(r.Context())
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	att, f, err := s.svc.Posts.Attachment(r.Context(), c.actor, id)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	defer func() {
		if closeErr := f.Close(); closeErr != nil {
			s.logger.Debug("closing a download failed", "pattern", r.Pattern, "err", closeErr)
		}
	}()
	files.ServeDownload(w, r, files.Download{
		FileName:    att.FileName,
		ContentType: att.ContentType,
		Inline:      att.IsImage,
		ModTime:     att.CreatedAt,
	}, f)
}

const tagUploadInvalid = "UploadInvalid"
