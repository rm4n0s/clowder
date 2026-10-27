package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"mime"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// document is the OpenAPI document, parsed once.
var document = sync.OnceValue(func() map[string]any {
	var doc map[string]any
	if err := json.Unmarshal(OpenAPI(), &doc); err != nil {
		panic("openapi.json is not valid JSON: " + err.Error())
	}
	return doc
})

// at walks a parsed JSON value by keys: maps by name. It returns nil when a
// key is missing.
func at(v any, keys ...string) any {
	for _, k := range keys {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[k]
	}
	return v
}

// validator checks a JSON value against a schema of the document. It knows
// the subset of JSON Schema that the document uses: type (a name or a list of
// names), properties, required, additionalProperties (a boolean or a schema),
// items, enum, const, format date-time, $ref, oneOf, minimum, maximum,
// minLength, maxLength, minItems and maxItems; the formats date-time and date. A keyword it does not know is a
// problem, so that the document cannot use one the tests would silently
// ignore.
type validator struct {
	root map[string]any
}

// knownKeywords are the keywords of a schema that the validator understands
// or that only describe.
var knownKeywords = map[string]bool{
	"type": true, "properties": true, "required": true, "additionalProperties": true, "items": true,
	"enum": true, "const": true, "format": true, "$ref": true, "oneOf": true, "minimum": true, "maximum": true,
	"minLength": true, "maxLength": true, "minItems": true, "maxItems": true,
	"description": true, "title": true, "example": true, "examples": true, "default": true,
}

// validate returns the problems of value against schema; path names where in
// the value we are (the root is "$").
func (v *validator) validate(schema any, value any, path string) []string {
	switch s := schema.(type) {
	case bool:
		if !s {
			return []string{path + ": not allowed"}
		}
		return nil
	case map[string]any:
		return v.validateObject(s, value, path)
	default:
		return []string{fmt.Sprintf("%s: the schema is a %T", path, schema)}
	}
}

func (v *validator) validateObject(s map[string]any, value any, path string) []string {
	for k := range s {
		if !knownKeywords[k] {
			return []string{fmt.Sprintf("%s: the validator does not know the schema keyword %q", path, k)}
		}
	}
	if ref, ok := s["$ref"].(string); ok {
		target := v.resolve(ref)
		if target == nil {
			return []string{fmt.Sprintf("%s: $ref %s does not resolve", path, ref)}
		}
		return v.validate(target, value, path)
	}
	var problems []string
	if t, ok := s["type"]; ok {
		if !typeMatches(t, value) {
			return []string{fmt.Sprintf("%s: %s is not of type %v", path, describe(value), t)}
		}
	}
	if enum, ok := s["enum"].([]any); ok {
		found := slices.ContainsFunc(enum, func(e any) bool { return reflect.DeepEqual(e, value) })
		if !found {
			problems = append(problems, fmt.Sprintf("%s: %s is not one of %v", path, describe(value), enum))
		}
	}
	if c, ok := s["const"]; ok && !reflect.DeepEqual(c, value) {
		problems = append(problems, fmt.Sprintf("%s: %s is not the constant %v", path, describe(value), c))
	}
	if s["format"] == "date-time" {
		if str, ok := value.(string); ok {
			if _, err := time.Parse(time.RFC3339, str); err != nil {
				problems = append(problems, fmt.Sprintf("%s: %q is not an RFC 3339 time", path, str))
			}
		}
	}
	if s["format"] == "date" {
		if str, ok := value.(string); ok {
			if d, err := time.Parse("2006-01-02", str); err != nil || d.Format("2006-01-02") != str {
				problems = append(problems, fmt.Sprintf("%s: %q is not a day (YYYY-MM-DD)", path, str))
			}
		}
	}
	if f, ok := value.(float64); ok {
		if min, ok := s["minimum"].(float64); ok && f < min {
			problems = append(problems, fmt.Sprintf("%s: %v is less than %v", path, f, min))
		}
		if max, ok := s["maximum"].(float64); ok && f > max {
			problems = append(problems, fmt.Sprintf("%s: %v is more than %v", path, f, max))
		}
	}
	if str, ok := value.(string); ok {
		n := float64(len([]rune(str)))
		if min, ok := s["minLength"].(float64); ok && n < min {
			problems = append(problems, fmt.Sprintf("%s: %q is shorter than %v", path, str, min))
		}
		if max, ok := s["maxLength"].(float64); ok && n > max {
			problems = append(problems, fmt.Sprintf("%s: %q is longer than %v", path, str, max))
		}
	}
	if arr, ok := value.([]any); ok {
		n := float64(len(arr))
		if min, ok := s["minItems"].(float64); ok && n < min {
			problems = append(problems, fmt.Sprintf("%s: %v items, at least %v wanted", path, n, min))
		}
		if max, ok := s["maxItems"].(float64); ok && n > max {
			problems = append(problems, fmt.Sprintf("%s: %v items, at most %v wanted", path, n, max))
		}
		if items, ok := s["items"]; ok {
			for i, item := range arr {
				problems = append(problems, v.validate(items, item, fmt.Sprintf("%s[%d]", path, i))...)
			}
		}
	}
	if obj, ok := value.(map[string]any); ok {
		problems = append(problems, v.validateProperties(s, obj, path)...)
	}
	if alts, ok := s["oneOf"].([]any); ok {
		matches := 0
		for _, alt := range alts {
			if len(v.validate(alt, value, path)) == 0 {
				matches++
			}
		}
		if matches != 1 {
			problems = append(problems, fmt.Sprintf("%s: matches %d of the oneOf schemas, want exactly 1", path, matches))
		}
	}
	return problems
}

func (v *validator) validateProperties(s map[string]any, obj map[string]any, path string) []string {
	var problems []string
	props, _ := s["properties"].(map[string]any)
	if required, ok := s["required"].([]any); ok {
		for _, r := range required {
			name, _ := r.(string)
			if _, there := obj[name]; !there {
				problems = append(problems, fmt.Sprintf("%s: the required property %q is missing", path, name))
			}
		}
	}
	names := make([]string, 0, len(obj))
	for name := range obj {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		sub := path + "." + name
		if propSchema, ok := props[name]; ok {
			problems = append(problems, v.validate(propSchema, obj[name], sub)...)
			continue
		}
		switch extra := s["additionalProperties"].(type) {
		case nil, bool:
			if extra == false {
				problems = append(problems, fmt.Sprintf("%s: the property %q is not in the schema", path, name))
			}
		default:
			problems = append(problems, v.validate(extra, obj[name], sub)...)
		}
	}
	return problems
}

// resolve follows a local reference (#/components/...). It returns nil when it
// does not resolve.
func (v *validator) resolve(ref string) any {
	rest, ok := strings.CutPrefix(ref, "#/")
	if !ok {
		return nil
	}
	return at(v.root, strings.Split(rest, "/")...)
}

// typeMatches reports whether value is of the type t, a name or a list of
// names of JSON Schema.
func typeMatches(t any, value any) bool {
	switch tt := t.(type) {
	case string:
		return isType(tt, value)
	case []any:
		for _, name := range tt {
			if s, ok := name.(string); ok && isType(s, value) {
				return true
			}
		}
	}
	return false
}

func isType(name string, value any) bool {
	switch name {
	case "string":
		_, ok := value.(string)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "number":
		_, ok := value.(float64)
		return ok
	case "integer":
		f, ok := value.(float64)
		return ok && f == math.Trunc(f)
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "null":
		return value == nil
	}
	return false
}

// describe shortens a value for a message.
func describe(value any) string {
	b, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf("%v", value)
	}
	if len(b) > 60 {
		return string(b[:60]) + "..."
	}
	return string(b)
}

// operationFor finds the operation of a request in the document: the method
// and the path of the pattern the request matched, without the method and the
// prefix of the server URL. It returns nil when there is none.
func operationFor(method, pattern string) map[string]any {
	_, path, ok := strings.Cut(pattern, " ")
	if !ok {
		return nil
	}
	path, ok = strings.CutPrefix(path, Prefix)
	if !ok {
		return nil
	}
	op, _ := at(document(), "paths", path, strings.ToLower(method)).(map[string]any)
	return op
}

// checkResponse checks an answer of the API against the OpenAPI document and
// fails the test when the document does not describe it (SPEC D45): the
// operation of the request (found by the pattern the mux matched, so the
// request must have been served by it) must have a response for the status,
// the Content-Type and the body must be those of the response, the body must
// validate against its schema, and the headers of the response must be there.
// A request that no route matched (the catch-all) must be a 404 or 405 with
// the Error schema.
//
// Every test of a route sends its requests through testEnv.send, which calls
// it; the tests of later steps do the same.
func checkResponse(t *testing.T, req *http.Request, rec *httptest.ResponseRecorder) {
	t.Helper()
	for _, p := range responseProblems(req, rec) {
		t.Error(p)
	}
}

// responseProblems is checkResponse returning its complaints.
func responseProblems(req *http.Request, rec *httptest.ResponseRecorder) []string {
	v := &validator{root: document()}
	name := req.Method + " " + req.URL.Path
	if req.Pattern == catchAllPattern {
		var problems []string
		if rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
			problems = append(problems, fmt.Sprintf("%s: the catch-all answered %d", name, rec.Code))
		}
		return append(problems, bodyProblems(v, rec, at(document(), "components", "schemas", "Error"), name)...)
	}
	op := operationFor(req.Method, req.Pattern)
	if op == nil {
		return []string{fmt.Sprintf("%s (pattern %q): no operation in the OpenAPI document", name, req.Pattern)}
	}
	responses, _ := op["responses"].(map[string]any)
	spec, ok := responses[fmt.Sprint(rec.Code)].(map[string]any)
	if !ok {
		return []string{fmt.Sprintf("%s: the document has no %d response (it has %v)", name, rec.Code, keysOf(responses))}
	}
	if ref, ok := spec["$ref"].(string); ok {
		spec, _ = v.resolve(ref).(map[string]any)
	}
	var problems []string
	headers, _ := at(spec, "headers").(map[string]any)
	for header := range headers {
		if rec.Header().Get(header) == "" {
			problems = append(problems, fmt.Sprintf("%s: the %d response should carry the header %s", name, rec.Code, header))
		}
	}
	content, _ := spec["content"].(map[string]any)
	if len(content) == 0 {
		if rec.Body.Len() != 0 {
			problems = append(problems, fmt.Sprintf("%s: the %d response has no content in the document but a body of %d bytes", name, rec.Code, rec.Body.Len()))
		}
		return problems
	}
	if _, isJSON := content["application/json"]; !isJSON {
		// A file or a plain text: the body is not checked, the type is.
		return append(problems, contentTypeProblems(content, rec, name)...)
	}
	return append(problems, bodyProblems(v, rec, at(content, "application/json", "schema"), name)...)
}

// contentTypeProblems checks that the Content-Type of rec is one of the media
// types of content, whose keys may be a type (text/plain), a range
// (image/*) or any (*/*).
func contentTypeProblems(content map[string]any, rec *httptest.ResponseRecorder, name string) []string {
	got, _, err := mime.ParseMediaType(rec.Header().Get("Content-Type"))
	if err != nil {
		return []string{fmt.Sprintf("%s: Content-Type = %q is not a media type", name, rec.Header().Get("Content-Type"))}
	}
	for want := range content {
		group, _, _ := strings.Cut(want, "/")
		if want == "*/*" || want == got || (strings.HasSuffix(want, "/*") && strings.HasPrefix(got, group+"/")) {
			return nil
		}
	}
	return []string{fmt.Sprintf("%s: Content-Type = %q, the document has %v", name, got, keysOf(content))}
}

// bodyProblems validates the JSON body of rec against schema.
func bodyProblems(v *validator, rec *httptest.ResponseRecorder, schema any, name string) []string {
	var problems []string
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		problems = append(problems, fmt.Sprintf("%s: Content-Type = %q, want application/json", name, ct))
	}
	if schema == nil {
		return append(problems, name+": the response has no application/json schema")
	}
	var value any
	if err := json.NewDecoder(bytes.NewReader(rec.Body.Bytes())).Decode(&value); err != nil {
		return append(problems, fmt.Sprintf("%s: the body is not JSON: %v: %s", name, err, rec.Body))
	}
	for _, p := range v.validate(schema, value, "$") {
		problems = append(problems, fmt.Sprintf("%s: the %d answer does not match the document: %s", name, rec.Code, p))
	}
	return problems
}

// keysOf returns the sorted keys of a map.
func keysOf(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
