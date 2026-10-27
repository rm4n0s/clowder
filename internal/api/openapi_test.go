package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/rm4n0s/clowder/internal/store"
)

// methodsOfPath are the HTTP methods an OpenAPI path item can hold.
var methodsOfPath = []string{"get", "put", "post", "delete", "options", "head", "patch", "trace"}

// operation is an operation of the document with where it is.
type operation struct {
	method string // upper case
	path   string // relative to the server URL
	spec   map[string]any
}

// operations lists every operation of the document.
func operations(t *testing.T) []operation {
	t.Helper()
	paths, _ := at(document(), "paths").(map[string]any)
	var ops []operation
	for path, item := range paths {
		items, _ := item.(map[string]any)
		for key, spec := range items {
			if !slices.Contains(methodsOfPath, key) {
				t.Errorf("path %s has the key %q, which is not a method", path, key)
				continue
			}
			s, _ := spec.(map[string]any)
			ops = append(ops, operation{method: strings.ToUpper(key), path: path, spec: s})
		}
	}
	sort.Slice(ops, func(i, j int) bool {
		if ops[i].path != ops[j].path {
			return ops[i].path < ops[j].path
		}
		return ops[i].method < ops[j].method
	})
	return ops
}

// TestOpenAPIMatchesRoutes checks, both ways, that every route of the API has
// an operation with the same method and path, and every operation has a route
// (SPEC D45). The catch-all is not an operation.
func TestOpenAPIMatchesRoutes(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	fromRoutes := map[string]bool{}
	for _, rt := range e.srv.Routes() {
		if rt.Pattern == catchAllPattern {
			continue
		}
		method, path, ok := strings.Cut(rt.Pattern, " ")
		rest, hasPrefix := strings.CutPrefix(path, Prefix)
		if !ok || !hasPrefix {
			t.Errorf("route %q is not METHOD /api/v1/path", rt.Pattern)
			continue
		}
		fromRoutes[method+" "+rest] = true
	}
	fromDocument := map[string]bool{}
	for _, op := range operations(t) {
		fromDocument[op.method+" "+op.path] = true
	}
	for key := range fromRoutes {
		if !fromDocument[key] {
			t.Errorf("the route %s has no operation in openapi.json", key)
		}
	}
	for key := range fromDocument {
		if !fromRoutes[key] {
			t.Errorf("openapi.json has the operation %s but no route", key)
		}
	}
	if len(fromRoutes) == 0 {
		t.Error("no routes")
	}
}

// TestRoutesEndWithTheCatchAll checks that the catch-all is the last route and
// the only one outside /api/v1/.
func TestRoutesEndWithTheCatchAll(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	routes := e.srv.Routes()
	if last := routes[len(routes)-1]; last.Pattern != "/api/" {
		t.Errorf("the last route is %q, want /api/", last.Pattern)
	}
	seen := map[string]bool{}
	for _, rt := range routes {
		if seen[rt.Pattern] {
			t.Errorf("the pattern %q is registered twice", rt.Pattern)
		}
		seen[rt.Pattern] = true
		if rt.Pattern != "/api/" && !strings.Contains(rt.Pattern, " "+Prefix+"/") {
			t.Errorf("the pattern %q is not under %s/", rt.Pattern, Prefix)
		}
		if rt.Handler == nil {
			t.Errorf("the route %q has no handler", rt.Pattern)
		}
	}
}

// TestOpenAPIReferences checks that every $ref of the document resolves, and
// that every schema and response of the components is used by something.
func TestOpenAPIReferences(t *testing.T) {
	t.Parallel()
	doc := document()
	v := &validator{root: doc}
	used := map[string]bool{}
	var walk func(node any, path string)
	walk = func(node any, path string) {
		switch n := node.(type) {
		case map[string]any:
			for k, child := range n {
				if k == "$ref" {
					ref, _ := child.(string)
					if v.resolve(ref) == nil {
						t.Errorf("%s: $ref %q does not resolve", path, ref)
					}
					used[ref] = true
					continue
				}
				walk(child, path+"/"+k)
			}
		case []any:
			for i, child := range n {
				walk(child, path+"/"+strconv.Itoa(i))
			}
		}
	}
	walk(doc, "#")
	for _, kind := range []string{"schemas", "responses"} {
		comps, _ := at(doc, "components", kind).(map[string]any)
		for name := range comps {
			if !used["#/components/"+kind+"/"+name] {
				t.Errorf("components.%s.%s is never referenced", kind, name)
			}
		}
	}
}

// TestOpenAPIOperations checks what every operation must have: a summary, a
// unique operationId, a success response and the error responses that apply:
//
//   - 500 on every operation;
//   - 401 on every operation that needs a login (its security is not empty);
//   - 413, 415 and 422 on every operation with a request body;
//   - 404 on every operation with a path parameter.
//
// It also checks that a path parameter of the path is declared, that the
// document uses the bearer scheme it declares, and that every schema of a
// response object refuses undocumented properties (additionalProperties:
// false), so that a field the handler adds fails the response check.
func TestOpenAPIOperations(t *testing.T) {
	t.Parallel()
	doc := document()
	if got := at(doc, "openapi"); got != "3.1.0" {
		t.Errorf("openapi = %v, want 3.1.0", got)
	}
	if got := at(doc, "info", "version"); got != "1.1.0" {
		t.Errorf("info.version = %v, want 1.1.0", got)
	}
	servers, _ := at(doc, "servers").([]any)
	if len(servers) != 1 || at(servers[0], "url") != "/api/v1" {
		t.Errorf("servers = %v, want [{url: /api/v1}]", servers)
	}
	if at(doc, "components", "securitySchemes", "bearer", "scheme") != "bearer" {
		t.Error("components.securitySchemes.bearer is not the bearer scheme")
	}
	globalSecurity, _ := at(doc, "security").([]any)
	if len(globalSecurity) != 1 {
		t.Errorf("the global security = %v, want the bearer scheme", globalSecurity)
	}

	ids := map[string]string{}
	for _, op := range operations(t) {
		name := op.method + " " + op.path
		if s, _ := op.spec["summary"].(string); s == "" {
			t.Errorf("%s has no summary", name)
		}
		id, _ := op.spec["operationId"].(string)
		if id == "" {
			t.Errorf("%s has no operationId", name)
		} else if other, dup := ids[id]; dup {
			t.Errorf("%s and %s have the operationId %q", name, other, id)
		}
		ids[id] = name

		responses, _ := op.spec["responses"].(map[string]any)
		has := func(code string) bool { _, ok := responses[code]; return ok }
		success := false
		for code := range responses {
			if strings.HasPrefix(code, "2") {
				success = true
			}
		}
		if !success {
			t.Errorf("%s has no 2xx response", name)
		}
		if !has("500") {
			t.Errorf("%s has no 500 response", name)
		}
		secured := true
		if sec, ok := op.spec["security"].([]any); ok && len(sec) == 0 {
			secured = false
		}
		if secured && !has("401") {
			t.Errorf("%s needs a login but has no 401 response", name)
		}
		if _, ok := op.spec["requestBody"]; ok {
			for _, code := range []string{"413", "415", "422"} {
				if !has(code) {
					t.Errorf("%s has a request body but no %s response", name, code)
				}
			}
			content, _ := at(op.spec, "requestBody", "content").(map[string]any)
			if _, ok := content["application/json"]; !ok {
				t.Errorf("%s: the request body is not application/json", name)
			}
		}
		declared := map[string]bool{}
		params, _ := op.spec["parameters"].([]any)
		for _, p := range params {
			if at(p, "in") == "path" {
				declared[at(p, "name").(string)] = true
			}
		}
		for _, seg := range strings.Split(op.path, "/") {
			if name2, ok := strings.CutPrefix(strings.TrimSuffix(seg, "}"), "{"); ok && strings.HasSuffix(seg, "}") {
				if !declared[name2] {
					t.Errorf("%s: the path parameter %s is not declared", name, name2)
				}
				if !has("404") {
					t.Errorf("%s has a path parameter but no 404 response", name)
				}
			}
		}
		for code := range responses {
			if len(code) != 3 || code[0] < '1' || code[0] > '5' {
				t.Errorf("%s: %q is not a status code", name, code)
			}
		}
	}
}

// TestOpenAPISchemasRefuseExtraProperties checks that every object schema in
// components.schemas that lists properties says additionalProperties: false.
func TestOpenAPISchemasRefuseExtraProperties(t *testing.T) {
	t.Parallel()
	schemas, _ := at(document(), "components", "schemas").(map[string]any)
	for name, schema := range schemas {
		s, _ := schema.(map[string]any)
		if s["type"] == "object" && s["properties"] != nil && s["additionalProperties"] != false {
			t.Errorf("components.schemas.%s lists properties but does not set additionalProperties: false", name)
		}
	}
}

// TestOpenAPIDocumentFormat checks that the file is pretty-printed with two
// spaces and ends with a newline, so that the diffs of a change stay small.
func TestOpenAPIDocumentFormat(t *testing.T) {
	t.Parallel()
	var v any
	if err := json.Unmarshal(OpenAPI(), &v); err != nil {
		t.Fatal(err)
	}
	raw := string(OpenAPI())
	if !strings.HasSuffix(raw, "}\n") {
		t.Error("openapi.json does not end with a newline")
	}
	if !strings.Contains(raw, "\n  \"openapi\": \"3.1.0\"") {
		t.Error("openapi.json is not indented with two spaces")
	}
}

// TestOpenAPIServed checks GET /openapi.json: no login, the embedded document.
func TestOpenAPIServed(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	rec := e.send(request(http.MethodGet, "/api/v1/openapi.json", nil))
	requireStatus(t, rec, http.StatusOK)
	if rec.Body.String() != string(OpenAPI()) {
		t.Error("the body is not the embedded document")
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
	head := e.sendRaw(request(http.MethodHead, "/api/v1/openapi.json", nil))
	requireStatus(t, head, http.StatusOK)
	if head.Body.Len() != 0 {
		t.Errorf("HEAD sent a body of %d bytes", head.Body.Len())
	}
}

// TestValidator tests the checker of the responses itself: it must accept
// what the schema describes and refuse what it does not, or the tests that
// rely on it would prove nothing.
func TestValidator(t *testing.T) {
	t.Parallel()
	root := map[string]any{"components": map[string]any{"schemas": map[string]any{
		"Name": map[string]any{"type": "string", "maxLength": float64(3)},
	}}}
	parse := func(s string) any {
		var v any
		if err := json.Unmarshal([]byte(s), &v); err != nil {
			t.Fatalf("bad test JSON %s: %v", s, err)
		}
		return v
	}
	tests := []struct {
		name   string
		schema string
		value  string
		want   string // a part of the first problem; empty means valid
	}{
		{"string", `{"type":"string"}`, `"a"`, ""},
		{"not a string", `{"type":"string"}`, `1`, "not of type"},
		{"integer", `{"type":"integer"}`, `3`, ""},
		{"a fraction is no integer", `{"type":"integer"}`, `3.5`, "not of type"},
		{"number", `{"type":"number"}`, `3.5`, ""},
		{"boolean", `{"type":"boolean"}`, `true`, ""},
		{"null in a list of types", `{"type":["string","null"]}`, `null`, ""},
		{"string in a list of types", `{"type":["string","null"]}`, `"x"`, ""},
		{"neither of the types", `{"type":["string","null"]}`, `1`, "not of type"},
		{"object", `{"type":"object"}`, `{"a":1}`, ""},
		{"array", `{"type":"array","items":{"type":"integer"}}`, `[1,2]`, ""},
		{"wrong item", `{"type":"array","items":{"type":"integer"}}`, `[1,"x"]`, "$[1]"},
		{"required there", `{"type":"object","required":["a"]}`, `{"a":1}`, ""},
		{"required missing", `{"type":"object","required":["a"]}`, `{}`, "required property"},
		{"extra property allowed by default", `{"type":"object","properties":{"a":{}}}`, `{"b":1}`, ""},
		{"extra property refused", `{"type":"object","properties":{"a":{}},"additionalProperties":false}`, `{"a":1,"b":1}`, `"b" is not in the schema`},
		{"additional properties by schema", `{"type":"object","additionalProperties":{"type":"string"}}`, `{"a":"x"}`, ""},
		{"wrong additional property", `{"type":"object","additionalProperties":{"type":"string"}}`, `{"a":1}`, "$.a"},
		{"nested property", `{"type":"object","properties":{"a":{"type":"object","properties":{"b":{"type":"string"}}}}}`, `{"a":{"b":1}}`, "$.a.b"},
		{"enum there", `{"enum":["a","b"]}`, `"a"`, ""},
		{"enum missing", `{"enum":["a","b"]}`, `"c"`, "not one of"},
		{"const", `{"const":true}`, `true`, ""},
		{"wrong const", `{"const":true}`, `false`, "constant"},
		{"date-time", `{"type":"string","format":"date-time"}`, `"2026-10-26T09:30:00Z"`, ""},
		{"not a date-time", `{"type":"string","format":"date-time"}`, `"yesterday"`, "RFC 3339"},
		{"a date without a time", `{"type":"string","format":"date-time"}`, `"2026-10-26"`, "RFC 3339"},
		{"minimum", `{"type":"integer","minimum":1}`, `0`, "less than"},
		{"maximum", `{"type":"integer","maximum":1}`, `2`, "more than"},
		{"maxLength counts characters", `{"type":"string","maxLength":2}`, `"éé"`, ""},
		{"too long", `{"type":"string","maxLength":2}`, `"abc"`, "longer than"},
		{"too short", `{"type":"string","minLength":2}`, `"a"`, "shorter than"},
		{"minItems", `{"type":"array","minItems":1}`, `[]`, "at least"},
		{"maxItems", `{"type":"array","maxItems":1}`, `[1,2]`, "at most"},
		{"oneOf one", `{"oneOf":[{"type":"string"},{"type":"integer"}]}`, `"a"`, ""},
		{"oneOf none", `{"oneOf":[{"type":"string"},{"type":"integer"}]}`, `true`, "oneOf"},
		{"oneOf two", `{"oneOf":[{"type":"number"},{"type":"integer"}]}`, `1`, "oneOf"},
		{"ref resolves", `{"$ref":"#/components/schemas/Name"}`, `"abc"`, ""},
		{"ref checks", `{"$ref":"#/components/schemas/Name"}`, `"abcd"`, "longer than"},
		{"ref missing", `{"$ref":"#/components/schemas/Nope"}`, `"a"`, "does not resolve"},
		{"unknown keyword", `{"pattern":"x"}`, `"a"`, "does not know"},
		{"false schema", `false`, `1`, "not allowed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := &validator{root: root}
			problems := v.validate(parse(tt.schema), parse(tt.value), "$")
			if tt.want == "" {
				if len(problems) != 0 {
					t.Fatalf("problems = %v, want none", problems)
				}
				return
			}
			if len(problems) == 0 || !strings.Contains(strings.Join(problems, "\n"), tt.want) {
				t.Fatalf("problems = %v, want one with %q", problems, tt.want)
			}
		})
	}
}

// TestOperationFor checks how a request finds its operation.
func TestOperationFor(t *testing.T) {
	t.Parallel()
	tests := []struct {
		method, pattern string
		found           bool
	}{
		{http.MethodPost, "POST /api/v1/auth/login", true},
		{http.MethodGet, "GET /api/v1/auth/invitations/{token}", true},
		{http.MethodDelete, "DELETE /api/v1/me/sessions/{id}", true},
		{http.MethodGet, "DELETE /api/v1/me/sessions/{id}", false},
		{http.MethodGet, "GET /api/v1/nope", false},
		{http.MethodGet, "/api/", false},
		{http.MethodGet, "", false},
	}
	for _, tt := range tests {
		if got := operationFor(tt.method, tt.pattern) != nil; got != tt.found {
			t.Errorf("operationFor(%s, %q) found = %v, want %v", tt.method, tt.pattern, got, tt.found)
		}
	}
}

// TestCheckResponseCatchesDifferences runs the check on answers that the
// document does not describe and expects it to complain, and to stay quiet
// about a good one.
func TestCheckResponseCatchesDifferences(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	token := e.token(e.addUser("alice", store.RoleMember).Username)
	good := request(http.MethodGet, "/api/v1/me", nil, bearer(token))
	if probs := responseProblems(good, e.sendRaw(good)); len(probs) != 0 {
		t.Fatalf("a good answer was refused: %v", probs)
	}

	setBody := func(mutate func(map[string]any)) func(*httptest.ResponseRecorder) {
		return func(rec *httptest.ResponseRecorder) {
			m := decode(t, rec)
			mutate(m)
			raw, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			rec.Body.Reset()
			rec.Body.Write(raw)
		}
	}
	tests := []struct {
		name   string
		mutate func(*httptest.ResponseRecorder)
		want   string
	}{
		{"an undocumented field", setBody(func(m map[string]any) { m["extra"] = 1 }), `"extra" is not in the schema`},
		{"an undocumented field in a nested object", setBody(func(m map[string]any) { m["user"].(map[string]any)["password_hash"] = "x" }), `"password_hash" is not in the schema`},
		{"a missing field", setBody(func(m map[string]any) { delete(m, "user") }), "required property"},
		{"a wrong type", setBody(func(m map[string]any) { m["unread_notifications"] = "3" }), "not of type"},
		{"an undocumented status", func(rec *httptest.ResponseRecorder) { rec.Code = http.StatusTeapot }, "no 418 response"},
		{"a wrong content type", func(rec *httptest.ResponseRecorder) { rec.Header().Set("Content-Type", "text/html") }, "Content-Type"},
		{"a body that is not JSON", func(rec *httptest.ResponseRecorder) { rec.Body.Reset(); rec.Body.WriteString("<html>") }, "not JSON"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := e.sendRaw(good)
			tt.mutate(rec)
			probs := responseProblems(good, rec)
			if !strings.Contains(strings.Join(probs, "\n"), tt.want) {
				t.Errorf("problems = %v, want one with %q", probs, tt.want)
			}
		})
	}

	// A header that the document promises.
	bad := request(http.MethodGet, "/api/v1/me", nil)
	rec := e.sendRaw(bad)
	rec.Header().Del("WWW-Authenticate")
	if probs := responseProblems(bad, rec); !strings.Contains(strings.Join(probs, "\n"), "WWW-Authenticate") {
		t.Errorf("problems = %v, want one about the missing WWW-Authenticate", probs)
	}
	// A body where the document has none.
	logout := request(http.MethodPost, "/api/v1/auth/logout", nil, bearer(token))
	rec = e.sendRaw(logout)
	requireStatus(t, rec, http.StatusNoContent)
	rec.Body.WriteString("x")
	if probs := responseProblems(logout, rec); !strings.Contains(strings.Join(probs, "\n"), "no content in the document") {
		t.Errorf("problems = %v, want one about a body on a 204", probs)
	}
}
