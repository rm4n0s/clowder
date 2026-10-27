package views

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// AuditDetail is one row of the details table of an audit event: a field and
// its old and new value, or a single text that spans both columns.
type AuditDetail struct {
	// Key is the name of the field.
	Key string
	// Old and New are the values before and after the change. They are empty
	// for a row with a Text.
	Old string
	New string
	// Text is the whole content of a row that has no old and new value, such as
	// "body changed (42 characters)" or the user of a member event. When it is
	// not empty the row spans the Old and New columns.
	Text string
}

// auditValueEmpty is how the details table shows an empty string, and
// auditValueNone how it shows a value that does not exist (null).
const (
	auditValueEmpty = "(empty)"
	auditValueNone  = "—"
)

// AuditDetails makes the rows of the details table from the decoded Details
// of an event (SPEC §7.10), sorted by key. A field of the form {"old": ...,
// "new": ...} is a row with the two values. The body shape of a post or
// comment, {"bodyChanged": true, "length": n}, is the one row "body changed
// (n characters)": the text of a body is never in the details. Any other
// value is a row with a text.
func AuditDetails(details map[string]any) []AuditDetail {
	keys := make([]string, 0, len(details))
	for k := range details {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var rows []AuditDetail
	bodyChanged, _ := details["bodyChanged"].(bool)
	for _, k := range keys {
		v := details[k]
		switch {
		case bodyChanged && k == "bodyChanged":
			text := "body changed"
			if n, ok := details["length"]; ok {
				text += " (" + auditText(n) + " " + characterWord(n) + ")"
			}
			rows = append(rows, AuditDetail{Key: "body", Text: text})
		case bodyChanged && k == "length":
			// Part of the body row.
		default:
			if m, ok := v.(map[string]any); ok {
				oldValue, hasOld := m["old"]
				newValue, hasNew := m["new"]
				if hasOld && hasNew && len(m) == 2 {
					rows = append(rows, AuditDetail{Key: k, Old: auditText(oldValue), New: auditText(newValue)})
					continue
				}
			}
			rows = append(rows, AuditDetail{Key: k, Text: auditText(v)})
		}
	}
	return rows
}

// characterWord is "character" for a length of 1 and "characters" otherwise.
func characterWord(length any) string {
	if n, ok := length.(float64); ok && n == 1 {
		return "character"
	}
	return "characters"
}

// auditText is the text of one value of the details.
func auditText(v any) string {
	switch x := v.(type) {
	case nil:
		return auditValueNone
	case string:
		if x == "" {
			return auditValueEmpty
		}
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	default:
		raw, err := json.Marshal(x)
		if err != nil {
			return fmt.Sprint(x)
		}
		return string(raw)
	}
}

// AuditFilterForm is the state of the filter form of the audit log: the
// values of the query parameters of the page. The zero value filters nothing.
type AuditFilterForm struct {
	// Room is the ID of the room to keep; 0 keeps every room.
	Room uint64
	// Actor is the ID of the user to keep; 0 keeps every user.
	Actor uint64
	// Entity is the entity type to keep; empty keeps every type.
	Entity string
	// Action is the action to keep; empty keeps every action.
	Action string
	// From and To are the first and the last day to keep, as 2006-01-02; empty
	// leaves that end open.
	From string
	To   string
	// Message is the problem with the filters, such as a date that is not a
	// date; empty shows none.
	Message string
}

// Query returns the query string of the filters (without the "?") for the
// page of the log, with empty filters left out. Page 1 and lower is not
// written.
func (f AuditFilterForm) Query(page int) string {
	q := url.Values{}
	if f.Room != 0 {
		q.Set("room", strconv.FormatUint(f.Room, 10))
	}
	if f.Actor != 0 {
		q.Set("actor", strconv.FormatUint(f.Actor, 10))
	}
	for name, value := range map[string]string{"entity": f.Entity, "action": f.Action, "from": f.From, "to": f.To} {
		if value != "" {
			q.Set(name, value)
		}
	}
	if page > 1 {
		q.Set("page", strconv.Itoa(page))
	}
	return q.Encode()
}

// AuditPath returns the path of the audit log page with the filters and the
// page number, such as /admin/audit?actor=3&page=2.
func (f AuditFilterForm) AuditPath(page int) string {
	if q := f.Query(page); q != "" {
		return "/admin/audit?" + q
	}
	return "/admin/audit"
}

// AdminRoomPath returns the path of the row of a room on the admin rooms
// page, /admin/rooms/<id>. Its actions are AdminRoomPath + "/rename",
// "/delete", "/members" and "/members/<user>/remove".
func AdminRoomPath(id uint64) string {
	return "/admin/rooms/" + strconv.FormatUint(id, 10)
}

// adminRoomLink is the link to a room's row on the admin rooms page.
func adminRoomLink(id uint64) string {
	return "/admin/rooms#room-" + strconv.FormatUint(id, 10)
}

// auditRoomLabel is the name of the room of an event, with the #.
func auditRoomLabel(name string) string {
	return "#" + strings.TrimPrefix(name, "#")
}

// AuditEntityLabel is the text of an entity type in the filter of the audit
// log: the events about accounts are "Accounts", every other type is shown as
// it is stored.
func AuditEntityLabel(entity string) string {
	if entity == "user" {
		return "Accounts"
	}
	return entity
}
