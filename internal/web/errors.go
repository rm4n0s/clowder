package web

import (
	"net/http"

	"github.com/a-h/templ"
	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/web/views"
	"github.com/rm4n0s/errors"
	"github.com/starfederation/datastar-go/datastar"
)

// Texts shown to people for the failures of a request. They are fixed text,
// never the text of an error (SPEC §9.1).
const (
	messageForbidden   = "You are not allowed to do that"
	messageConflict    = "That clashes with a change made just now. Reload the page and try again"
	messageInvalid     = "Some of what you sent is not valid"
	messageRateLimited = "Too many attempts. Try again in a few minutes."
	// messageTooLarge is the page of an upload over the limit.
	messageTooLarge = "That upload is too large"
	// messageTryAgain is shown in a form when its submit fails for a reason
	// the person cannot fix by editing it.
	messageTryAgain = "Something went wrong. Try again."
	// messageInvitationInvalid is the page of an invitation that is used,
	// expired or unknown.
	messageInvitationInvalid = "This invitation is not valid."
	// messageResetInvalid is the page of a password reset link that is used,
	// expired, unknown or of a user who is disabled.
	messageResetInvalid = "This reset link is not valid."
)

// statusOf maps an error to the HTTP status of its answer by the tags of the
// app package (SPEC §9.1): NotFound 404, Forbidden 403, InvalidInput 422,
// Conflict 409, RateLimited 429, TooLarge 413, and 500 for anything else. Unauthenticated is
// not a status but a redirect to the login page; statusOf says 302 for it, and
// fail and the form handlers redirect.
func statusOf(err error) int {
	appErr, ok := errors.FromError(err)
	if !ok {
		return http.StatusInternalServerError
	}
	switch appErr.Tag {
	case app.TagNotFound:
		return http.StatusNotFound
	case app.TagForbidden:
		return http.StatusForbidden
	case app.TagInvalidInput, app.TagSSHKeyTypeRefused, app.TagSSHKeyTooShort:
		return http.StatusUnprocessableEntity
	case app.TagConflict:
		return http.StatusConflict
	case app.TagRateLimited:
		return http.StatusTooManyRequests
	case app.TagTooLarge:
		return http.StatusRequestEntityTooLarge
	case app.TagUnauthenticated:
		return http.StatusFound
	default:
		return http.StatusInternalServerError
	}
}

// messageOf returns the text for people of an error status.
func messageOf(status int) string {
	switch status {
	case http.StatusNotFound:
		return messageNotFound
	case http.StatusForbidden:
		return messageForbidden
	case http.StatusUnprocessableEntity:
		return messageInvalid
	case http.StatusConflict:
		return messageConflict
	case http.StatusTooManyRequests:
		return messageRateLimited
	case http.StatusRequestEntityTooLarge:
		return messageTooLarge
	default:
		return messageInternalError
	}
}

// fail answers a request whose service call failed with err: a redirect to the
// login page for TagUnauthenticated, else the error page with the status of
// statusOf. An error that is a 500 is logged with its route; the page never
// shows the error. A Datastar client drops the body of a non-200 answer, so
// the forms use formFailed, which answers 200.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	status := statusOf(err)
	if status == http.StatusFound {
		s.redirectToLogin(w, r)
		return
	}
	if status == http.StatusInternalServerError {
		s.logFailure(r, err)
	}
	s.render(w, r, status, views.ErrorPage(status, messageOf(status)))
}

// logFailure logs an error that ends in a 500, with the route pattern.
func (s *Server) logFailure(r *http.Request, err error) {
	s.logger.Error("request failed", "pattern", r.Pattern, "err", err)
}

// formView is what a form handler can answer with: the form alone, which a
// Datastar request patches into the page, and the whole page, for a request
// that is a plain form post (a browser without JavaScript).
type formView struct {
	fragment templ.Component
	page     templ.Component
}

// formFailed answers a form submit that failed with err. The person's input is
// kept and the problems are shown:
//
//   - TagInvalidInput shows the messages of the metadata "fields" next to
//     their inputs, and the one named FieldForm above the form; so do the tags
//     of a refused SSH key and a TagConflict that has such messages;
//   - TagTooLarge shows "Each file can be at most N MB." next to the files;
//   - TagRateLimited shows messageRateLimited above the form;
//   - TagForbidden and TagConflict show messageForbidden and messageConflict;
//   - any other error shows messageTryAgain (a 500 is logged with its route);
//   - TagUnauthenticated redirects to the login page.
//
// build makes the views from the problems. A Datastar request gets the form as
// an SSE patch with status 200, because the client ignores the body of any
// other status; a plain request gets the page with the status of statusOf.
func (s *Server) formFailed(w http.ResponseWriter, r *http.Request, err error, build func(views.FormErrors) formView) {
	status := statusOf(err)
	var problems views.FormErrors
	switch {
	case status == http.StatusFound:
		s.redirectToLogin(w, r)
		return
	case isTag(err, app.TagInvalidInput), isTag(err, app.TagTooLarge), isTag(err, app.TagSSHKeyTypeRefused), isTag(err, app.TagSSHKeyTooShort):
		problems = formErrorsOf(err)
	case isTag(err, app.TagConflict) && hasFields(err):
		// A conflict that says what clashes (a key that is registered, the
		// limit of keys) shows its message next to the field.
		problems = formErrorsOf(err)
	case isTag(err, app.TagRateLimited):
		problems.Form = messageRateLimited
	case isTag(err, app.TagForbidden):
		problems.Form = messageForbidden
	case isTag(err, app.TagConflict):
		problems.Form = messageConflict
	default:
		if status == http.StatusInternalServerError {
			s.logFailure(r, err)
		}
		problems.Form = messageTryAgain
	}
	v := build(problems)
	if isDatastar(r) {
		s.patch(w, r, v.fragment)
		return
	}
	s.render(w, r, status, v.page)
}

// hasFields reports whether err carries messages for fields in its metadata.
func hasFields(err error) bool {
	appErr, ok := errors.FromError(err)
	if !ok {
		return false
	}
	fields, _ := appErr.Metadata["fields"].(map[string]string)
	return len(fields) > 0
}

// formErrorsOf reads the metadata "fields" of a TagInvalidInput error: the
// message of FieldForm goes to Form, the others stay with their field. A
// TagTooLarge error becomes the message of the field "attachments" with the
// limit of its metadata.
func formErrorsOf(err error) views.FormErrors {
	appErr, ok := errors.FromError(err)
	if !ok {
		return views.FormErrors{}
	}
	if appErr.Tag == app.TagTooLarge {
		limit, _ := appErr.Metadata["limit"].(int64)
		return views.FormErrors{Fields: map[string]string{
			app.FieldAttachments: "Each file can be at most " + views.SizeText(limit) + ".",
		}}
	}
	fields, _ := appErr.Metadata["fields"].(map[string]string)
	problems := views.FormErrors{Form: fields[app.FieldForm], Fields: map[string]string{}}
	for name, message := range fields {
		if name != app.FieldForm {
			problems.Fields[name] = message
		}
	}
	return problems
}

// patch answers a Datastar request with one SSE event that patches the
// element with the id of c into the page.
func (s *Server) patch(w http.ResponseWriter, r *http.Request, c templ.Component) {
	sse := datastar.NewSSE(w, r)
	if err := sse.PatchElementTempl(c); err != nil {
		appErr := errors.NewErr(tagSSEWriteFailed, err).SetMetadata("event", "patch-elements")
		s.logger.Debug("sse write failed", "err", appErr)
	}
}

// redirect sends the browser to target. A Datastar request gets an SSE
// redirect with status 200 (the client does not follow an HTTP redirect of
// its fetch); a plain request gets an HTTP redirect with the status.
func (s *Server) redirect(w http.ResponseWriter, r *http.Request, target string, status int) {
	if !isDatastar(r) {
		http.Redirect(w, r, target, status)
		return
	}
	sse := datastar.NewSSE(w, r)
	if err := sseRedirect(sse, target); err != nil {
		appErr := errors.NewErr(tagSSEWriteFailed, err).SetMetadata("event", "redirect")
		s.logger.Debug("sse write failed", "err", appErr)
	}
}

const tagSSEWriteFailed = "SSEWriteFailed"
