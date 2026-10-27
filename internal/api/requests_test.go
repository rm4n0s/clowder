package api

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/errors"
)

// jsonNames returns the JSON names of the fields of a struct type.
func jsonNames(t *testing.T, v any) []string {
	t.Helper()
	typ := reflect.TypeOf(v)
	var names []string
	for i := 0; i < typ.NumField(); i++ {
		name, _, _ := strings.Cut(typ.Field(i).Tag.Get("json"), ",")
		if name == "" || name == "-" {
			t.Fatalf("%s.%s has no json name", typ.Name(), typ.Field(i).Name)
		}
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// TestRequestSchemasHaveTheFieldsOfTheStructs checks that every request body
// of the rooms and the posts is described by a schema whose properties are the
// JSON names of the struct that reads it, and whose required fields are
// among them.
func TestRequestSchemasHaveTheFieldsOfTheStructs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		schema string
		value  any
	}{
		{"RoomCreateRequest", roomCreateRequest{}},
		{"RoomPatchRequest", roomPatchRequest{}},
		{"MemberAddRequest", memberAddRequest{}},
		{"PostRequest", postRequest{}},
		{"TaskRequest", taskRequest{}},
	}
	for _, tt := range tests {
		t.Run(tt.schema, func(t *testing.T) {
			t.Parallel()
			schema, _ := at(document(), "components", "schemas", tt.schema).(map[string]any)
			if schema == nil {
				t.Fatalf("no schema %s", tt.schema)
			}
			props, _ := schema["properties"].(map[string]any)
			got := keysOf(props)
			if want := jsonNames(t, tt.value); !reflect.DeepEqual(got, want) {
				t.Errorf("properties = %v, want %v", got, want)
			}
			required, _ := schema["required"].([]any)
			for _, name := range required {
				if _, ok := props[name.(string)]; !ok {
					t.Errorf("%v is required but not a property", name)
				}
			}
		})
	}
}

// TestFieldNamesAreTheNamesOfTheDocument is the table of how the field names of
// the services become the names of the JSON of the API: each is a property of
// the request it belongs to (or a parameter of the path).
func TestFieldNamesAreTheNamesOfTheDocument(t *testing.T) {
	t.Parallel()
	properties := func(schema string) []string {
		props, _ := at(document(), "components", "schemas", schema, "properties").(map[string]any)
		return keysOf(props)
	}
	tests := []struct {
		field string // the name in the errors of the services
		want  string // the name of the JSON
		in    []string
	}{
		{app.FieldName, "name", properties("RoomCreateRequest")},
		{app.FieldDescription, "description", properties("RoomCreateRequest")},
		{app.FieldVisibility, "visibility", properties("RoomCreateRequest")},
		{app.FieldUser, "user_id", properties("MemberAddRequest")},
		{app.FieldBody, "body", properties("PostRequest")},
		{app.FieldFormat, "format", properties("PostRequest")},
		{app.FieldTags, "tags", properties("PostRequest")},
		{app.FieldTasks, "tasks", properties("PostRequest")},
		{app.FieldAttachments, "upload_ids", properties("PostRequest")},
		{app.FieldEmoji, "emoji", []string{"emoji"}},
		{app.FieldDueDate, "due_date", properties("TaskRequest")},
		{app.FieldTitle, "title", properties("TaskRequest")},
		{app.FieldDisplayName, "display_name", properties("SignupRequest")},
		{app.FieldClientName, "client_name", properties("LoginRequest")},
	}
	for _, tt := range tests {
		t.Run(tt.field, func(t *testing.T) {
			t.Parallel()
			if got := apiFieldName(tt.field); got != tt.want {
				t.Errorf("apiFieldName(%q) = %q, want %q", tt.field, got, tt.want)
			}
			if !slices.Contains(tt.in, tt.want) {
				t.Errorf("%q is not in %v", tt.want, tt.in)
			}
		})
	}
	// The edit names the attachments by their own field.
	if !slices.Contains(properties("PostRequest"), "attachment_ids") {
		t.Error("PostRequest has no attachment_ids")
	}
}

func TestRenameField(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		err    error
		want   map[string]string
		sameAs bool
	}{
		{"the field is there", errors.New(app.TagInvalidInput, "m", "fields", map[string]string{app.FieldAttachments: "too many", "body": "x"}),
			map[string]string{"attachment_ids": "too many", "body": "x"}, false},
		{"the field is not there", errors.New(app.TagInvalidInput, "m", "fields", map[string]string{"body": "x"}),
			map[string]string{"body": "x"}, false},
		{"another tag", errors.New(app.TagForbidden, "m", "fields", map[string]string{app.FieldAttachments: "x"}), nil, true},
		{"no fields", errors.New(app.TagInvalidInput, "m"), nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := renameField(tt.err, app.FieldAttachments, "attachment_ids")
			appErr, ok := errors.FromError(got)
			if !ok {
				t.Fatalf("not tagged: %v", got)
			}
			if tt.sameAs {
				if appErr.Tag != tt.err.(*errors.Error).Tag {
					t.Errorf("tag = %q", appErr.Tag)
				}
				return
			}
			if fields, _ := appErr.Metadata["fields"].(map[string]string); !reflect.DeepEqual(fields, tt.want) {
				t.Errorf("fields = %v, want %v", fields, tt.want)
			}
			if appErr != tt.err {
				t.Error("the error is not the one that was given: its tag and route must stay")
			}
		})
	}
}

// TestRoutesOfTheErrorsOfTheAPI checks the route of every error tag that the
// rooms and the posts make in this package.
func TestRoutesOfTheErrorsOfTheAPI(t *testing.T) {
	t.Parallel()
	query := func(q string) *http.Request { return httptest.NewRequest(http.MethodGet, "/x?"+q, nil) }
	str := func(s string) *string { return &s }
	tests := []struct {
		name  string
		tag   string
		route string
		run   func() error
		field string
	}{
		{"a before that is zero", tagBeforeInvalid, "feedParams.BeforeInvalid", func() error { _, _, err := feedParams(query("before=0")); return err }, "before"},
		{"a before that is text", tagBeforeInvalid, "feedParams.BeforeInvalid", func() error { _, _, err := feedParams(query("before=x")); return err }, "before"},
		{"a limit of zero", tagLimitInvalid, "feedParams.LimitInvalid", func() error { _, _, err := feedParams(query("limit=0")); return err }, "limit"},
		{"a limit over 100", tagLimitInvalid, "feedParams.LimitInvalid", func() error { _, _, err := feedParams(query("limit=101")); return err }, "limit"},
		{"a limit that is text", tagLimitInvalid, "feedParams.LimitInvalid", func() error { _, _, err := feedParams(query("limit=x")); return err }, "limit"},
		{"a due date that is no day", tagDueDateInvalid, "postInput.DueDateInvalid", func() error {
			_, err := postInput(postRequest{Tasks: &[]taskRequest{{Title: "t", DueDate: str("soon")}}}, nil)
			return err
		}, "tasks"},
		{"upload IDs", tagUploadsUnavailable, "postInput.UploadsUnavailable", func() error {
			_, err := postInput(postRequest{UploadIDs: []string{"a"}}, nil)
			return err
		}, "upload_ids"},
		{"attachments to keep on a new post", tagKeepOnCreate, "postInput.KeepOnCreate", func() error {
			_, err := postInput(postRequest{AttachmentIDs: &[]uint64{3}}, nil)
			return err
		}, "attachment_ids"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.run()
			appErr := requireRoute(t, err, tt.tag, tt.route)
			fields, _ := appErr.Metadata["fields"].(map[string]string)
			if fields[tt.field] == "" || len(fields) != 1 {
				t.Errorf("fields = %v, want one for %q", fields, tt.field)
			}
			// Each is 422 invalid_input with the field.
			p, internal := problemOf(err)
			if internal || p.status != http.StatusUnprocessableEntity || p.object.Code != codeInvalidInput || p.object.Fields[tt.field] == "" {
				t.Errorf("problem = %+v, internal %v", p, internal)
			}
		})
	}
}

// TestPostInputMergesAnEditWithThePost checks the rules of PATCH at the level
// of the input: what the body leaves out is the post's own.
func TestPostInputMergesAnEditWithThePost(t *testing.T) {
	t.Parallel()
	current := app.Post{
		Format: app.FormatMarkdown, Body: "old", Tags: []string{"a"},
		Tasks:       []app.TaskCard{{ID: 7, Title: "t", Assignees: []app.UserRef{{ID: 3}, {ID: 4}}}},
		Attachments: []app.AttachmentView{{ID: 9}},
	}
	got, err := postInput(postRequest{}, &current)
	if err != nil {
		t.Fatal(err)
	}
	want := app.PostInput{
		Format: app.FormatMarkdown, Body: "old", Tags: []string{"a"}, KeepAttachments: []uint64{9},
		Tasks: []app.TaskInput{{ID: 7, Title: "t", Assignees: []uint64{3, 4}}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("input = %+v, want %+v", got, want)
	}
	body, format := "new", "text"
	got, err = postInput(postRequest{Body: &body, Format: &format, Tags: &[]string{}, Tasks: &[]taskRequest{}, AttachmentIDs: &[]uint64{}}, &current)
	if err != nil {
		t.Fatal(err)
	}
	if got.Body != "new" || got.Format != app.FormatText || len(got.Tags) != 0 || got.Tasks == nil || len(got.Tasks) != 0 || len(got.KeepAttachments) != 0 {
		t.Errorf("input = %+v", got)
	}
	// A new post starts empty, as text.
	got, err = postInput(postRequest{}, nil)
	if err != nil || got.Format != app.FormatText || got.Body != "" || got.Tags != nil || got.Tasks != nil || got.KeepAttachments != nil {
		t.Errorf("input = %+v, %v", got, err)
	}
}

// TestNewNeedsEveryService checks that the server is not made without a
// service.
func TestNewNeedsEveryService(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	full := e.srv.svc
	tests := []struct {
		name   string
		mutate func(*Services)
	}{
		{"no accounts", func(s *Services) { s.Accounts = nil }},
		{"no notifications", func(s *Services) { s.Notifications = nil }},
		{"no messages", func(s *Services) { s.Messages = nil }},
		{"no rooms", func(s *Services) { s.Rooms = nil }},
		{"no posts", func(s *Services) { s.Posts = nil }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			svc := full
			tt.mutate(&svc)
			_, err := New(Options{Services: svc})
			_ = requireRoute(t, err, tagServicesMissing, "New.ServicesMissing")
		})
	}
}
