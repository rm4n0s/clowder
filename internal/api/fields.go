package api

import (
	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/errors"
)

// fieldNames maps the names of fields in the errors of the services (app.Field*)
// to the names of the JSON of the API, where snake_case of the service's name
// is not the API's. It is the one table: problemOf applies it to every
// invalid_input answer, and a field that is not in it is named by its name in
// snake_case (dueDate is due_date). The tasks of a post are one field, tasks,
// whose message names the row ("Task 2: ..."). The attachments of a post are
// upload_ids on a new post and a new comment; the edit says attachment_ids
// (see renameField).
var fieldNames = map[string]string{
	app.FieldUser:        "user_id",
	app.FieldAttachments: "upload_ids",
}

// apiFieldName returns the name of the field in the JSON of the API.
func apiFieldName(name string) string {
	if api, ok := fieldNames[name]; ok {
		return api
	}
	return snakeCase(name)
}

// renameField returns err with the field from named to to in its field
// errors, for a route whose JSON calls a field of the service by another name
// than fieldNames does. Any other error comes back as it is. The error keeps
// its tag and its route.
func renameField(err error, from, to string) error {
	appErr, ok := errors.FromError(err)
	if !ok || appErr.Tag != app.TagInvalidInput {
		return err
	}
	fields, ok := appErr.Metadata["fields"].(map[string]string)
	if !ok {
		return err
	}
	message, there := fields[from]
	if !there {
		return err
	}
	renamed := make(map[string]string, len(fields))
	for name, m := range fields {
		renamed[name] = m
	}
	delete(renamed, from)
	renamed[to] = message
	_ = appErr.SetMetadata("fields", renamed)
	return appErr
}
