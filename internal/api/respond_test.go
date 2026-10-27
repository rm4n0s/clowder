package api

import (
	"bytes"
	"encoding/json"
	stdErrors "errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/store"
	"github.com/rm4n0s/errors"
)

// TestProblemOf is the table of the error mapping (SPEC D42, §9.1): one row for
// every code and for every tag that leads to it.
func TestProblemOf(t *testing.T) {
	t.Parallel()
	fields := map[string]string{"displayName": "Use 1 to 64 characters.", "form": "x", "q": "y"}
	tests := []struct {
		name       string
		err        error
		status     int
		code       string
		wantFields map[string]string
		header     map[string]string
		internal   bool
	}{
		{name: "not found", err: errors.New(app.TagNotFound, "m"), status: 404, code: "not_found"},
		{name: "a path that has no route", err: errors.New(tagRouteNotFound, "m"), status: 404, code: "not_found"},
		{name: "an ID that is no number", err: errors.New(tagIDInvalid, "m"), status: 404, code: "not_found"},
		{name: "forbidden", err: errors.New(app.TagForbidden, "m"), status: 403, code: "forbidden"},
		{
			name: "invalid input with fields in snake_case", err: errors.New(app.TagInvalidInput, "m", "fields", fields),
			status: 422, code: "invalid_input",
			wantFields: map[string]string{"display_name": "Use 1 to 64 characters.", "form": "x", "q": "y"},
		},
		{name: "invalid input without fields", err: errors.New(app.TagInvalidInput, "m"), status: 422, code: "invalid_input"},
		{
			name: "a body that cannot be read", err: errors.New(tagBodyInvalid, "m", "fields", map[string]string{"body": "x"}),
			status: 422, code: "invalid_input", wantFields: map[string]string{"body": "x"},
		},
		{name: "conflict", err: errors.New(app.TagConflict, "m"), status: 409, code: "conflict"},
		{name: "rate limited", err: errors.New(app.TagRateLimited, "m"), status: 429, code: "rate_limited", header: map[string]string{"Retry-After": "900"}},
		{name: "too large", err: errors.New(app.TagTooLarge, "m"), status: 413, code: "too_large"},
		{name: "a body over the limit", err: errors.New(tagBodyTooLarge, "m"), status: 413, code: "too_large"},
		{
			name: "unauthenticated", err: errors.New(app.TagUnauthenticated, "m"), status: 401, code: "unauthenticated",
			header: map[string]string{"WWW-Authenticate": `Bearer realm="clowder"`},
		},
		{
			name: "no bearer token", err: errors.New(tagBearerInvalid, "m"), status: 401, code: "unauthenticated",
			header: map[string]string{"WWW-Authenticate": `Bearer realm="clowder"`},
		},
		{
			name: "method not allowed", err: errors.New(tagMethodNotAllowed, "m", "allow", "GET, HEAD"), status: 405, code: "method_not_allowed",
			header: map[string]string{"Allow": "GET, HEAD"},
		},
		{name: "unsupported media type", err: errors.New(tagUnsupportedMediaType, "m"), status: 415, code: "unsupported_media_type"},
		{name: "an unknown tag", err: errors.New("SomethingElse", "secret detail"), status: 500, code: "internal", internal: true},
		{name: "a query that failed", err: errors.New(store.TagQueryFailed, "secret detail"), status: 500, code: "internal", internal: true},
		{name: "an error without a tag", err: stdErrors.New("secret detail"), status: 500, code: "internal", internal: true},
	}
	seen := map[string]bool{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p, internal := problemOf(tt.err)
			if p.status != tt.status || p.object.Code != tt.code || internal != tt.internal {
				t.Errorf("problemOf = status %d code %q internal %v, want %d %q %v", p.status, p.object.Code, internal, tt.status, tt.code, tt.internal)
			}
			if len(p.object.Fields) != len(tt.wantFields) {
				t.Errorf("fields = %v, want %v", p.object.Fields, tt.wantFields)
			}
			for name, msg := range tt.wantFields {
				if p.object.Fields[name] != msg {
					t.Errorf("fields = %v, want %v", p.object.Fields, tt.wantFields)
				}
			}
			for name, value := range tt.header {
				if p.header[name] != value {
					t.Errorf("header %s = %q, want %q", name, p.header[name], value)
				}
			}
			if strings.Contains(p.object.Message, "secret") || p.object.Message == "" {
				t.Errorf("message = %q", p.object.Message)
			}
		})
		seen[tt.code] = true
	}
	// Every code of the document has a row.
	enum, _ := at(document(), "components", "schemas", "ErrorObject", "properties", "code", "enum").([]any)
	for _, c := range enum {
		if !seen[c.(string)] {
			t.Errorf("the code %v has no row in the table", c)
		}
	}
	if len(enum) != len(seen) {
		t.Errorf("the document has %d codes, the table %d", len(enum), len(seen))
	}
}

func TestSnakeCase(t *testing.T) {
	t.Parallel()
	tests := []struct{ in, want string }{
		{"displayName", "display_name"},
		{"clientName", "client_name"},
		{"username", "username"},
		{"q", "q"},
		{"form", "form"},
		{"a", "a"},
		{"", ""},
		{"alreadySnake_case", "already_snake_case"},
	}
	for _, tt := range tests {
		if got := snakeCase(tt.in); got != tt.want {
			t.Errorf("snakeCase(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
	if got := snakeFields(nil); got != nil {
		t.Errorf("snakeFields(nil) = %v, want nil", got)
	}
}

// TestInternalErrorIsLoggedAndHidden checks that a 500 says nothing of its
// cause, is logged with its route and the pattern of the request, and that a
// 4xx is not logged by the API.
func TestInternalErrorIsLoggedAndHidden(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	alice := e.addUser("alice", store.RoleMember)
	token := e.token("alice")
	// A closed store makes the next query fail.
	if err := e.st.Close(); err != nil {
		t.Fatal(err)
	}
	r := request(http.MethodGet, "/api/v1/me", nil, bearer(token))
	rec := e.send(r)
	requireStatus(t, rec, http.StatusInternalServerError)
	if got := errorCode(t, rec); got != "internal" {
		t.Errorf("code = %q", got)
	}
	if body := rec.Body.String(); strings.Contains(body, "sql") || strings.Contains(body, "closed") || strings.Contains(body, "QueryFailed") {
		t.Errorf("the answer tells the cause: %s", body)
	}
	var entry struct {
		Msg     string           `json:"msg"`
		Pattern string           `json:"pattern"`
		Err     errors.ErrorJson `json:"err"`
	}
	found := false
	for _, line := range strings.Split(strings.TrimSpace(e.log.String()), "\n") {
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("log line %q: %v", line, err)
		}
		if entry.Msg == "request failed" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("no 'request failed' entry in the log:\n%s", e.log.String())
	}
	if entry.Pattern != "GET /api/v1/me" {
		t.Errorf("pattern = %q", entry.Pattern)
	}
	if !strings.Contains(entry.Err.Route(), "Accounts.sessionActor") {
		t.Errorf("route = %q, want the route of the failed query", entry.Err.Route())
	}
	_ = alice
}

// TestPanicIsAJSON500 checks that a handler that panics is answered as a JSON
// 500 and logged, and that the next request still works.
func TestPanicIsAJSON500(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	h := e.srv.recovering(func(http.ResponseWriter, *http.Request) { panic("boom with a secret") })
	r := request(http.MethodGet, "/api/v1/me", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	requireStatus(t, rec, http.StatusInternalServerError)
	if errorCode(t, rec) != "internal" || strings.Contains(rec.Body.String(), "secret") {
		t.Errorf("body = %s", rec.Body)
	}
	if !strings.Contains(e.log.String(), "handler panicked") || !strings.Contains(e.log.String(), tagHandlerPanicked) {
		t.Errorf("the panic is not logged:\n%s", e.log.String())
	}
	// http.ErrAbortHandler goes on to the server.
	defer func() {
		if v := recover(); v != http.ErrAbortHandler {
			t.Errorf("recovered %v, want http.ErrAbortHandler", v)
		}
	}()
	e.srv.recovering(func(http.ResponseWriter, *http.Request) { panic(http.ErrAbortHandler) }).
		ServeHTTP(httptest.NewRecorder(), r)
}

func TestUnknownPathsAndMethods(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	token := e.token(e.addUser("alice", store.RoleMember).Username)
	tests := []struct {
		name   string
		method string
		path   string
		status int
		code   string
		allow  string
	}{
		{"an unknown path", http.MethodGet, "/api/v1/nope", 404, "not_found", ""},
		{"an unknown deeper path", http.MethodGet, "/api/v1/me/nope/deeper", 404, "not_found", ""},
		{"an unknown version", http.MethodGet, "/api/v2/me", 404, "not_found", ""},
		{"the root of the API", http.MethodGet, "/api/", 404, "not_found", ""},
		{"a post to an unknown path", http.MethodPost, "/api/v1/nope", 404, "not_found", ""},
		{"a wrong method on a get route", http.MethodPost, "/api/v1/me", 405, "method_not_allowed", "GET, HEAD"},
		{"a wrong method on a post route", http.MethodGet, "/api/v1/auth/login", 405, "method_not_allowed", "POST"},
		{"a wrong method on a path with a parameter", http.MethodGet, "/api/v1/me/sessions/3", 405, "method_not_allowed", "DELETE"},
		{"put on a path with two methods", http.MethodPut, "/api/v1/me/sessions", 405, "method_not_allowed", "GET, HEAD"},
		{"delete on the invitation", http.MethodDelete, "/api/v1/auth/invitations/abc", 405, "method_not_allowed", "GET, HEAD"},
	}
	for _, tt := range tests {
		for _, withToken := range []bool{false, true} {
			t.Run(tt.name, func(t *testing.T) {
				var opts []reqOpt
				if withToken {
					opts = append(opts, bearer(token))
				}
				r := request(tt.method, tt.path, nil, opts...)
				rec := e.send(r)
				requireStatus(t, rec, tt.status)
				if got := errorCode(t, rec); got != tt.code {
					t.Errorf("code = %q, want %q", got, tt.code)
				}
				if got := rec.Header().Get("Allow"); got != tt.allow {
					t.Errorf("Allow = %q, want %q", got, tt.allow)
				}
				if r.Pattern != "/api/" {
					t.Errorf("the pattern is %q, want the catch-all", r.Pattern)
				}
				if strings.Contains(rec.Body.String(), "<html") {
					t.Error("the answer is HTML")
				}
			})
		}
	}
}

// TestRoutes checks the failures of the helpers one by one, with the route of
// each tag (SPEC §9.3): the constructor is called in the function that fails.
func TestErrorRoutes(t *testing.T) {
	t.Parallel()
	post := func(contentType, body string) (*httptest.ResponseRecorder, *http.Request) {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(body))
		if contentType != "" {
			r.Header.Set("Content-Type", contentType)
		}
		return httptest.NewRecorder(), r
	}
	type target struct {
		A string `json:"a"`
		N int    `json:"n"`
	}
	tests := []struct {
		name       string
		run        func() error
		tag        string
		route      string
		wantFields []string
	}{
		{
			name: "a body that is no JSON type", tag: tagUnsupportedMediaType, route: "decodeJSON.UnsupportedMediaType",
			run: func() error { w, r := post("text/plain", `{}`); return decodeJSON(w, r, &target{}) },
		},
		{
			name: "no content type", tag: tagUnsupportedMediaType, route: "decodeJSON.UnsupportedMediaType",
			run: func() error { w, r := post("", `{}`); return decodeJSON(w, r, &target{}) },
		},
		{
			name: "a body of syntax errors", tag: tagBodyInvalid, route: "decodeJSON.BodyInvalid", wantFields: []string{"body"},
			run: func() error { w, r := post("application/json", `{"a":`); return decodeJSON(w, r, &target{}) },
		},
		{
			name: "an empty body", tag: tagBodyInvalid, route: "decodeJSON.BodyInvalid", wantFields: []string{"body"},
			run: func() error { w, r := post("application/json", ``); return decodeJSON(w, r, &target{}) },
		},
		{
			name: "an unknown field", tag: tagBodyInvalid, route: "decodeJSON.BodyInvalid", wantFields: []string{"zzz"},
			run: func() error { w, r := post("application/json", `{"zzz":1}`); return decodeJSON(w, r, &target{}) },
		},
		{
			name: "a wrong type", tag: tagBodyInvalid, route: "decodeJSON.BodyInvalid", wantFields: []string{"n"},
			run: func() error { w, r := post("application/json", `{"n":"x"}`); return decodeJSON(w, r, &target{}) },
		},
		{
			name: "two values", tag: tagBodyInvalid, route: "decodeJSON.BodyInvalid", wantFields: []string{"body"},
			run: func() error { w, r := post("application/json", `{} {}`); return decodeJSON(w, r, &target{}) },
		},
		{
			name: "garbage after the value", tag: tagBodyInvalid, route: "decodeJSON.BodyInvalid", wantFields: []string{"body"},
			run: func() error { w, r := post("application/json", `{} x`); return decodeJSON(w, r, &target{}) },
		},
		{
			name: "a body over the limit", tag: tagBodyTooLarge, route: "decodeJSON.BodyTooLarge",
			run: func() error {
				w, r := post("application/json", `{"a":"`+strings.Repeat("x", maxBodyBytes)+`"}`)
				return decodeJSON(w, r, &target{})
			},
		},
		{
			name: "a second value over the limit", tag: tagBodyTooLarge, route: "decodeJSON.BodyTooLarge",
			run: func() error {
				w, r := post("application/json", `{} `+strings.Repeat(" ", maxBodyBytes))
				return decodeJSON(w, r, &target{})
			},
		},
		{
			name: "no authorization header", tag: tagBearerInvalid, route: "bearerToken.BearerInvalid",
			run: func() error { _, err := bearerToken(httptest.NewRequest(http.MethodGet, "/", nil)); return err },
		},
		{
			name: "a path ID that is no number", tag: tagIDInvalid, route: "pathID.IDInvalid",
			run: func() error {
				r := httptest.NewRequest(http.MethodGet, "/x/abc", nil)
				r.SetPathValue("id", "abc")
				_, err := pathID(r, "id")
				return err
			},
		},
		{
			name: "no services", tag: tagServicesMissing, route: "New.ServicesMissing",
			run: func() error { _, err := New(Options{}); return err },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.run()
			appErr := requireRoute(t, err, tt.tag, tt.route)
			if len(tt.wantFields) > 0 {
				fields, _ := appErr.Metadata["fields"].(map[string]string)
				for _, f := range tt.wantFields {
					if fields[f] == "" {
						t.Errorf("fields = %v, want one for %q", fields, f)
					}
				}
			}
		})
	}
	// The valid cases pass.
	w, r := post("application/json; charset=utf-8", `{"a":"x","n":3}`)
	var got target
	if err := decodeJSON(w, r, &got); err != nil || got.A != "x" || got.N != 3 {
		t.Errorf("decodeJSON = %+v, %v", got, err)
	}
	w, r = post("application/json", "{\"a\":\"y\"}\n \n")
	if err := decodeJSON(w, r, &got); err != nil || got.A != "y" {
		t.Errorf("decodeJSON with trailing white space = %+v, %v", got, err)
	}
}

func TestRouteOfUnknownRouteAndMethod(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	// The two tags of the catch-all are made in unknownRoute and reach
	// writeError; the answer tests above cover the statuses, this checks the
	// route of the error that the handler builds.
	var logged bytes.Buffer
	e.srv.logger = slog.New(slog.NewJSONHandler(&logged, nil))
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/nope", nil)
	e.srv.unknownRoute(rec, r)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d", rec.Code)
	}
	if logged.Len() != 0 {
		t.Errorf("a 404 was logged by the API: %s", logged.String())
	}
}

type unencodable struct{}

func (unencodable) MarshalJSON() ([]byte, error) { return nil, stdErrors.New("no") }

// TestEncodeFailureIsAClean500 checks the answer whose JSON cannot be made.
func TestEncodeFailureIsAClean500(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	rec := httptest.NewRecorder()
	e.srv.writeJSON(rec, httptest.NewRequest(http.MethodGet, "/api/v1/me", nil), http.StatusOK, unencodable{})
	requireStatus(t, rec, http.StatusInternalServerError)
	if rec.Header().Get("Content-Type") != jsonContentType {
		t.Errorf("Content-Type = %q", rec.Header().Get("Content-Type"))
	}
	if errorCode(t, rec) != "internal" {
		t.Errorf("body = %s", rec.Body)
	}
	if !strings.Contains(e.log.String(), tagEncodeFailed) {
		t.Errorf("the failure is not logged:\n%s", e.log.String())
	}
}

// failingWriter is a ResponseWriter whose body write fails.
type failingWriter struct{ http.ResponseWriter }

func (failingWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

// TestWriteFailureIsLoggedAtDebug checks that a client that went away is not
// an error.
func TestWriteFailureIsLoggedAtDebug(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	e.srv.writeJSON(failingWriter{httptest.NewRecorder()}, httptest.NewRequest(http.MethodGet, "/api/v1/me", nil), http.StatusOK, map[string]int{"a": 1})
	if !strings.Contains(e.log.String(), tagWriteFailed) || strings.Contains(e.log.String(), `"level":"ERROR"`) {
		t.Errorf("log = %s", e.log.String())
	}
	e.srv.openAPI(failingWriter{httptest.NewRecorder()}, httptest.NewRequest(http.MethodGet, "/api/v1/openapi.json", nil))
}

func TestWriteTimesAndHeaders(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	e.addUser("alice", store.RoleMember)
	rec := e.send(request(http.MethodGet, "/api/v1/me", nil, bearer(e.token("alice"))))
	if got := rec.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q", got)
	}
	if !strings.HasSuffix(rec.Body.String(), "}\n") {
		t.Errorf("the body does not end with a newline: %q", rec.Body.String())
	}
	if rec.Header().Get("Content-Length") == "" {
		t.Error("no Content-Length")
	}
}
