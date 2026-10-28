package api

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"mime"
	"net/http"
	"reflect"
	"runtime/debug"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/errors"
)

const (
	// maxBodyBytes is the largest JSON body of a request (SPEC §7.12).
	maxBodyBytes = 64 << 10
	// jsonContentType is the Content-Type of every answer with a body.
	jsonContentType = "application/json; charset=utf-8"
)

// The codes of the error object (SPEC D42).
const (
	codeNotFound             = "not_found"
	codeForbidden            = "forbidden"
	codeInvalidInput         = "invalid_input"
	codeConflict             = "conflict"
	codeRateLimited          = "rate_limited"
	codeTooLarge             = "too_large"
	codeUnauthenticated      = "unauthenticated"
	codeMethodNotAllowed     = "method_not_allowed"
	codeUnsupportedMediaType = "unsupported_media_type"
	codeInternal             = "internal"
)

// The messages of the error object: fixed text, never the text of an error.
const (
	messageNotFound          = "Not found."
	messageForbidden         = "You are not allowed to do that."
	messageInvalidInput      = "Some of what you sent is not valid."
	messageConflict          = "That clashes with a change made just now."
	messageRateLimited       = "Too many attempts. Try again later."
	messageTooLarge          = "That is too large."
	messageUnauthenticated   = "Log in to use this."
	messageWrongCredentials  = "Wrong username or password."
	messageMethodNotAllowed  = "That method is not allowed here."
	messageUnsupportedMedia  = "Send the body as application/json."
	messageInternal          = "Something went wrong."
	messageBodyNotJSON       = "The body is not valid JSON."
	messageBodyEmpty         = "The body is empty."
	messageBodyMoreThanOne   = "The body must be exactly one JSON value."
	messageBodyUnknownField  = "Unknown field."
	messageBodyWrongType     = "Wrong type."
	messageBodyIDTooLarge    = "Not an ID."
	wwwAuthenticateChallenge = `Bearer realm="clowder"`
)

// errorBody is the JSON of every error answer.
type errorBody struct {
	Error errorObject `json:"error"`
}

// errorObject is the error of an errorBody.
type errorObject struct {
	Code    string            `json:"code"`
	Message string            `json:"message"`
	Fields  map[string]string `json:"fields,omitempty"`
}

// problem is an error as it is answered: the status, the error object and the
// headers that go with it.
type problem struct {
	status int
	object errorObject
	header map[string]string
}

// problemOf maps an error to the answer of the API (SPEC D42, §9.1):
//
//   - app.TagNotFound 404 not_found, app.TagForbidden 403 forbidden,
//     app.TagInvalidInput 422 invalid_input with the field errors of the
//     metadata "fields" (named in snake_case; so do app.TagSSHKeyTypeRefused
//     and app.TagSSHKeyTooShort), app.TagConflict 409 conflict (with the
//     "fields" it carries, if any),
//     app.TagRateLimited 429 rate_limited with Retry-After, app.TagTooLarge
//     413 too_large;
//   - app.TagUnauthenticated and a missing or malformed bearer header 401
//     unauthenticated with WWW-Authenticate;
//   - the tags of this package: a body over the limit 413 too_large, a body
//     that cannot be read, a query before or limit that is wrong, a due date
//     that is not a day, a field that must be there and is not, a wrong page,
//     a form of an upload that is not one file in the field "file" and
//     attachment_ids on a new post, the people of a conversation named in
//     both lists, in neither or as no one but the caller, a name for a direct
//     conversation, a filter of the audit log that is not valid, a change
//     of a user, of the profile or of the settings that changes nothing, and
//     a message of the WebSocket that is
//     not valid or names a topic that is not 422 invalid_input, a Content-Type that is not JSON
//     415 unsupported_media_type, an unknown path 404 not_found, a wrong
//     method 405 method_not_allowed with Allow;
//   - anything else, and an error that is not tagged, 500 internal, whose
//     message says nothing about the cause.
//
// The second result is true for a 500, which the caller logs with its route.
func problemOf(err error) (problem, bool) {
	appErr, ok := errors.FromError(err)
	if !ok {
		return internalProblem(), true
	}
	switch appErr.Tag {
	case app.TagNotFound, tagRouteNotFound, tagIDInvalid:
		return problem{status: http.StatusNotFound, object: errorObject{Code: codeNotFound, Message: messageNotFound}}, false
	case app.TagForbidden:
		return problem{status: http.StatusForbidden, object: errorObject{Code: codeForbidden, Message: messageForbidden}}, false
	case app.TagInvalidInput, app.TagSSHKeyTypeRefused, app.TagSSHKeyTooShort:
		fields, _ := appErr.Metadata["fields"].(map[string]string)
		return problem{status: http.StatusUnprocessableEntity,
			object: errorObject{Code: codeInvalidInput, Message: messageInvalidInput, Fields: renamedFields(fields, apiFieldName)}}, false
	case tagBodyInvalid, tagBeforeInvalid, tagLimitInvalid, tagDueDateInvalid, tagKeepOnCreate,
		tagUploadInvalid, tagFieldMissing, tagPageInvalid, tagTaskDueInvalid, tagListsInvalid, tagNoOthers, tagDirectName,
		tagFilterInvalid, tagAdminUserInvalid, tagProfileEmpty, tagSettingsEmpty, tagPrefsEmpty, tagFormatInvalid, tagUserIDsInvalid, tagTopicInvalid, tagTopicsTooMany, tagSocketMessageInvalid:
		fields, _ := appErr.Metadata["fields"].(map[string]string)
		return problem{status: http.StatusUnprocessableEntity,
			object: errorObject{Code: codeInvalidInput, Message: messageInvalidInput, Fields: renamedFields(fields, snakeCase)}}, false
	case app.TagConflict:
		// A conflict may say what clashes, with the messages of "fields" (an
		// SSH key that is registered, the limit of keys).
		fields, _ := appErr.Metadata["fields"].(map[string]string)
		return problem{status: http.StatusConflict,
			object: errorObject{Code: codeConflict, Message: messageConflict, Fields: renamedFields(fields, apiFieldName)}}, false
	case app.TagRateLimited:
		return problem{status: http.StatusTooManyRequests,
			object: errorObject{Code: codeRateLimited, Message: messageRateLimited},
			header: map[string]string{"Retry-After": strconv.Itoa(int(app.LoginWindow / time.Second))}}, false
	case app.TagTooLarge, tagBodyTooLarge:
		return problem{status: http.StatusRequestEntityTooLarge, object: errorObject{Code: codeTooLarge, Message: messageTooLarge}}, false
	case app.TagUnauthenticated, tagBearerInvalid:
		return unauthenticatedProblem(messageUnauthenticated), false
	case tagMethodNotAllowed:
		allow, _ := appErr.Metadata["allow"].(string)
		return problem{status: http.StatusMethodNotAllowed,
			object: errorObject{Code: codeMethodNotAllowed, Message: messageMethodNotAllowed},
			header: map[string]string{"Allow": allow}}, false
	case tagUnsupportedMediaType:
		return problem{status: http.StatusUnsupportedMediaType,
			object: errorObject{Code: codeUnsupportedMediaType, Message: messageUnsupportedMedia}}, false
	default:
		return internalProblem(), true
	}
}

// internalProblem is the answer to an error that is the server's fault.
func internalProblem() problem {
	return problem{status: http.StatusInternalServerError, object: errorObject{Code: codeInternal, Message: messageInternal}}
}

// unauthenticatedProblem is the 401 answer with the given message.
func unauthenticatedProblem(message string) problem {
	return problem{status: http.StatusUnauthorized,
		object: errorObject{Code: codeUnauthenticated, Message: message},
		header: map[string]string{"WWW-Authenticate": wwwAuthenticateChallenge}}
}

// renamedFields returns the field errors with each name run through rename;
// it returns nil for none. The fields of a service's TagInvalidInput are named
// by apiFieldName, those of the API's own errors (a body, a query) already
// carry the client's names and only get snake_case (a client may have sent
// displayName; the answer says display_name).
func renamedFields(fields map[string]string, rename func(string) string) map[string]string {
	if len(fields) == 0 {
		return nil
	}
	out := make(map[string]string, len(fields))
	for name, message := range fields {
		out[rename(name)] = message
	}
	return out
}

// snakeCase turns camelCase into snake_case.
func snakeCase(name string) string {
	var b strings.Builder
	for i, r := range name {
		if unicode.IsUpper(r) {
			if i > 0 {
				b.WriteByte('_')
			}
			r = unicode.ToLower(r)
		}
		b.WriteRune(r)
	}
	return b.String()
}

// writeError answers a request whose handler failed with err, as problemOf
// says. A 500 is logged with its route and the pattern of the request; the
// answer never holds the error.
func (s *Server) writeError(w http.ResponseWriter, r *http.Request, err error) {
	p, internal := problemOf(err)
	if internal {
		s.logger.Error("request failed", "pattern", r.Pattern, "err", err)
	}
	s.writeProblem(w, r, p)
}

// writeProblem sends a problem as the JSON error object.
func (s *Server) writeProblem(w http.ResponseWriter, r *http.Request, p problem) {
	for name, value := range p.header {
		w.Header().Set(name, value)
	}
	s.writeJSON(w, r, p.status, errorBody{Error: p.object})
}

// writeJSON answers with the status and v as JSON. The body is made before
// anything is sent, so that a value that cannot be encoded becomes a clean
// 500 and never a half-written answer.
func (s *Server) writeJSON(w http.ResponseWriter, r *http.Request, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		appErr := errors.NewErr(tagEncodeFailed, err).SetMetadata("status", status)
		s.logger.Error("encoding the answer failed", "pattern", r.Pattern, "err", appErr)
		body = []byte(`{"error":{"code":"internal","message":"` + messageInternal + `"}}`)
		status = http.StatusInternalServerError
	}
	w.Header().Set("Content-Type", jsonContentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(body)+1))
	w.WriteHeader(status)
	if r.Method == http.MethodHead {
		return
	}
	if _, err := w.Write(append(body, '\n')); err != nil {
		// Usually the client went away; nothing more can be sent.
		appErr := errors.NewErr(tagWriteFailed, err).SetMetadata("status", status)
		s.logger.Debug("writing the answer failed", "pattern", r.Pattern, "err", appErr)
	}
}

// writeNoContent answers 204.
func (s *Server) writeNoContent(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNoContent)
}

// decodeJSON reads the JSON body of a request into dst. The Content-Type must
// be application/json (415 otherwise), the body at most 64 KiB, it holds
// exactly one JSON value and no field dst does not have. A failure is
// tagBodyTooLarge, tagUnsupportedMediaType or tagBodyInvalid, with the
// metadata "fields" naming the field or "body".
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" || (params["charset"] != "" && !strings.EqualFold(params["charset"], "utf-8")) {
		return errors.New(tagUnsupportedMediaType, "the body is not application/json",
			"contentType", r.Header.Get("Content-Type"))
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return errors.NewErr(tagBodyTooLarge, err).SetMetadata("limit", int64(maxBodyBytes))
		}
		return errors.NewErr(tagBodyInvalid, err).SetMetadata("fields", decodeProblems(err))
	}
	var extra json.RawMessage
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New(tagBodyInvalid, "the body holds more than one value",
				"fields", map[string]string{"body": messageBodyMoreThanOne})
		}
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return errors.NewErr(tagBodyTooLarge, err).SetMetadata("limit", int64(maxBodyBytes))
		}
		return errors.NewErr(tagBodyInvalid, err).SetMetadata("fields", decodeProblems(err))
	}
	if name, bad := oversizedID(reflect.ValueOf(dst), ""); bad {
		return errors.New(tagBodyInvalid, "a number in the body is larger than any ID",
			"fields", map[string]string{name: messageBodyIDTooLarge})
	}
	return nil
}

// maxID is the largest ID the databases hold: the columns are 64-bit signed
// integers, and a larger number in a query fails in the driver (a 500) instead
// of finding nothing.
const maxID = math.MaxInt64

// oversizedID looks through the decoded body v for an unsigned number above
// maxID, which only an ID can be. It returns the JSON name of the top-level
// field that holds it (name is that field's name while it descends).
func oversizedID(v reflect.Value, name string) (string, bool) {
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if v.IsNil() {
			return "", false
		}
		return oversizedID(v.Elem(), name)
	case reflect.Struct:
		for i := range v.NumField() {
			field := v.Type().Field(i)
			if !field.IsExported() {
				continue
			}
			jsonName, _, _ := strings.Cut(field.Tag.Get("json"), ",")
			if name != "" || jsonName == "" {
				jsonName = name
			}
			if got, bad := oversizedID(v.Field(i), jsonName); bad {
				return got, true
			}
		}
	case reflect.Slice, reflect.Array:
		for i := range v.Len() {
			if got, bad := oversizedID(v.Index(i), name); bad {
				return got, true
			}
		}
	case reflect.Uint, reflect.Uint64:
		if v.Uint() > maxID {
			return name, true
		}
	}
	return "", false
}

// decodeProblems says what is wrong with a body that json.Decoder refused: a
// field that dst does not have or has with another type is named, anything
// else is a problem of "body".
func decodeProblems(err error) map[string]string {
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) && typeErr.Field != "" {
		return map[string]string{typeErr.Field: messageBodyWrongType}
	}
	if rest, ok := strings.CutPrefix(err.Error(), "json: unknown field "); ok {
		if name, err := strconv.Unquote(rest); err == nil {
			return map[string]string{name: messageBodyUnknownField}
		}
	}
	if err == io.EOF {
		return map[string]string{"body": messageBodyEmpty}
	}
	return map[string]string{"body": messageBodyNotJSON}
}

// recovering wraps h so that a panic is logged and answered as the JSON 500,
// not as the HTML page of the web server.
func (s *Server) recovering(h func(http.ResponseWriter, *http.Request)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			v := recover()
			if v == nil {
				return
			}
			// http.ErrAbortHandler is the way a handler aborts an answer on
			// purpose; net/http expects it to reach the server.
			if err, ok := v.(error); ok && errors.Is(err, http.ErrAbortHandler) {
				panic(v)
			}
			appErr := errors.New(tagHandlerPanicked, "handler panicked",
				"panic", fmt.Sprint(v), "stack", string(debug.Stack()))
			s.logger.Error("handler panicked", "pattern", r.Pattern, "err", appErr)
			s.writeProblem(w, r, internalProblem())
		}()
		h(w, r)
	})
}

// rfc3339 formats a time as RFC 3339 in UTC, to the second.
func rfc3339(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

const (
	tagBodyInvalid          = "BodyInvalid"
	tagBodyTooLarge         = "BodyTooLarge"
	tagUnsupportedMediaType = "UnsupportedMediaType"
	tagEncodeFailed         = "EncodeFailed"
	tagWriteFailed          = "WriteFailed"
	tagHandlerPanicked      = "HandlerPanicked"
)
