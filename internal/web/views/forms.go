package views

import (
	"net/url"
	"strings"
)

// FormErrors are the problems to show in a form after a failed submit. The
// zero value shows none.
type FormErrors struct {
	// Form is a problem of the whole form, shown above its fields.
	Form string
	// Fields maps the name of an input to the message shown next to it.
	Fields map[string]string
}

// formField is one labeled input of a form, as the FormField component draws
// it.
type formField struct {
	// ID is the id of the input; the message has the id ID + "-error".
	ID string
	// Name is the name of the input, which is also the key of its message.
	Name string
	// Label is the text of the label.
	Label string
	// Type is the type of the input.
	Type string
	// Value is the value kept from the last submit; never drawn for a password.
	Value string
	// Autocomplete is the autocomplete hint for password managers.
	Autocomplete string
	// Error is the message shown next to the input, or empty.
	Error string
	// Hint is a line of help under the input, shown only while the input has
	// no error (the error says it better).
	Hint string
	// Autofocus puts the cursor in the input when the page opens.
	Autofocus bool
}

// FormPost returns the Datastar action that sends the form it is on to path
// with a POST, as an ordinary form (not as signals). Put it on the form as
// data-on:submit. The path is escaped, so that it can sit inside the quotes of
// the expression.
func FormPost(path string) string {
	return "@post('" + escapeQuote(path) + "', {contentType: 'form'})"
}

// escapeQuote replaces the characters that would end the quoted string of a
// Datastar expression.
func escapeQuote(s string) string {
	return strings.NewReplacer(`\`, "%5C", `'`, "%27").Replace(s)
}

// invitePath returns the path of the sign-up page and form of a token.
func invitePath(token string) string {
	return "/invite/" + url.PathEscape(token)
}
