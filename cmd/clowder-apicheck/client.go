package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/rm4n0s/errors"
)

// maxAnswerBytes bounds how much of an answer the client reads.
const maxAnswerBytes = 4 << 20

// redactor replaces the secrets of a run in everything that is printed: the
// JSON fields that hold one, and the values it was told about.
type redactor struct {
	secrets []string
}

// add tells the redactor a value to hide wherever it appears. Short or empty
// values are not added, so that "a" does not turn every letter into <token>.
func (r *redactor) add(secret string) {
	if len(secret) >= 8 {
		r.secrets = append(r.secrets, secret)
	}
}

// text hides the known secrets in s with <secret>.
func (r *redactor) text(s string) string {
	for _, secret := range r.secrets {
		s = strings.ReplaceAll(s, secret, "<secret>")
	}
	return s
}

// redactJSON returns v (a decoded JSON value) with the fields that hold a
// secret replaced: token and invitation_token by <token>, password by
// <password>. It copies the maps and slices it changes.
func redactJSON(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, child := range x {
			switch k {
			case "token", "invitation_token":
				out[k] = "<token>"
			case "password":
				out[k] = "<password>"
			default:
				out[k] = redactJSON(child)
			}
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, child := range x {
			out[i] = redactJSON(child)
		}
		return out
	default:
		return v
	}
}

// printable makes the text that is printed for a JSON body: the body decoded,
// its secrets replaced, and indented. A body that is not JSON is printed as it
// is, with the known secrets hidden.
func (r *redactor) printable(body []byte) string {
	if len(bytes.TrimSpace(body)) == 0 {
		return ""
	}
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		return r.text(strings.TrimSpace(string(body)))
	}
	var pretty bytes.Buffer
	enc := json.NewEncoder(&pretty)
	enc.SetEscapeHTML(false) // <token> stays <token>
	enc.SetIndent("", "  ")
	if err := enc.Encode(redactJSON(v)); err != nil {
		return r.text(strings.TrimSpace(string(body)))
	}
	return r.text(strings.TrimSpace(pretty.String()))
}

// client sends requests to the API and prints them.
type client struct {
	base  string
	http  *http.Client
	out   io.Writer
	redac *redactor
	// token is the bearer token of the session; empty before the login.
	token string
	// requests counts the requests made.
	requests int
}

// answer is what a request got.
type answer struct {
	status int
	header http.Header
	body   []byte
	// json is the body decoded, nil when it is empty or not JSON.
	json any
}

// field returns the value at the path of keys in the decoded JSON of the
// answer, nil when there is none.
func (a answer) field(keys ...string) any {
	v := a.json
	for _, k := range keys {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[k]
	}
	return v
}

// do sends the request, prints it and the answer, and fails with tagStatusUnexpected
// when the status is not want. A body that is not nil is sent as JSON. When the
// client has a token it is sent as the bearer.
func (c *client) do(ctx context.Context, method, path string, body any, want int) (answer, error) {
	var payload []byte
	if body != nil {
		var err error
		payload, err = json.Marshal(body)
		if err != nil {
			return answer{}, errors.NewErr(tagRequestEncodeFailed, err).SetMetadata("path", path)
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(payload))
	if err != nil {
		return answer{}, errors.NewErr(tagRequestFailed, err).SetMetadata("method", method, "path", path)
	}
	c.requests++
	_, _ = fmt.Fprintf(c.out, "> %s %s\n", method, path)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
		_, _ = fmt.Fprintln(c.out, "> Authorization: Bearer <token>")
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
		_, _ = fmt.Fprintln(c.out, "> Content-Type: application/json")
		_, _ = fmt.Fprintln(c.out, indent(c.redac.printable(payload), "> "))
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return answer{}, errors.NewErr(tagRequestFailed, err).SetMetadata("method", method, "path", path)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswerBytes))
	if err != nil {
		return answer{}, errors.NewErr(tagRequestFailed, err).SetMetadata("method", method, "path", path)
	}
	_, _ = fmt.Fprintf(c.out, "< %d %s\n", resp.StatusCode, http.StatusText(resp.StatusCode))
	for _, h := range []string{"Retry-After", "WWW-Authenticate", "Allow"} {
		if v := resp.Header.Get(h); v != "" {
			_, _ = fmt.Fprintf(c.out, "< %s: %s\n", h, v)
		}
	}
	if text := c.redac.printable(raw); text != "" {
		_, _ = fmt.Fprintln(c.out, indent(text, "< "))
	}
	_, _ = fmt.Fprintln(c.out)
	a := answer{status: resp.StatusCode, header: resp.Header, body: raw}
	if len(raw) > 0 {
		var v any
		if json.Unmarshal(raw, &v) == nil {
			a.json = v
		}
	}
	if resp.StatusCode != want {
		return a, errors.New(tagStatusUnexpected, "the server answered with another status",
			"method", method, "path", path, "want", want, "got", resp.StatusCode)
	}
	return a, nil
}

// indent puts prefix at the start of every line of text.
func indent(text, prefix string) string {
	return prefix + strings.ReplaceAll(text, "\n", "\n"+prefix)
}

const (
	tagRequestEncodeFailed = "RequestEncodeFailed"
	tagRequestFailed       = "RequestFailed"
	tagStatusUnexpected    = "StatusUnexpected"
)
