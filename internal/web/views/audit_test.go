package views_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/web/views"
)

func TestAuditDetails(t *testing.T) {
	tests := []struct {
		name    string
		details map[string]any
		want    []views.AuditDetail
	}{
		{name: "no details", details: map[string]any{}, want: nil},
		{
			name:    "a change has the old and the new value",
			details: map[string]any{"name": map[string]any{"old": "general", "new": "lobby"}},
			want:    []views.AuditDetail{{Key: "name", Old: "general", New: "lobby"}},
		},
		{
			name: "null is a dash and the empty string says so, sorted by key",
			details: map[string]any{
				"visibility":  map[string]any{"old": nil, "new": "private"},
				"description": map[string]any{"old": "", "new": nil},
			},
			want: []views.AuditDetail{
				{Key: "description", Old: "(empty)", New: "—"},
				{Key: "visibility", Old: "—", New: "private"},
			},
		},
		{
			name:    "the body of a post is its length, never text",
			details: map[string]any{"bodyChanged": true, "length": float64(42)},
			want:    []views.AuditDetail{{Key: "body", Text: "body changed (42 characters)"}},
		},
		{
			name:    "a body of one character",
			details: map[string]any{"bodyChanged": true, "length": float64(1)},
			want:    []views.AuditDetail{{Key: "body", Text: "body changed (1 character)"}},
		},
		{
			name:    "a body change without a length",
			details: map[string]any{"bodyChanged": true},
			want:    []views.AuditDetail{{Key: "body", Text: "body changed"}},
		},
		{
			name:    "a length alone is a plain value",
			details: map[string]any{"length": float64(3)},
			want:    []views.AuditDetail{{Key: "length", Text: "3"}},
		},
		{
			name:    "the user of a member event",
			details: map[string]any{"userID": float64(7), "username": "bob"},
			want:    []views.AuditDetail{{Key: "userID", Text: "7"}, {Key: "username", Text: "bob"}},
		},
		{
			name:    "numbers, booleans and other values",
			details: map[string]any{"a": 1.5, "b": true, "c": []any{"x", float64(1)}, "d": map[string]any{"old": "only old"}},
			want: []views.AuditDetail{
				{Key: "a", Text: "1.5"}, {Key: "b", Text: "true"}, {Key: "c", Text: `["x",1]`}, {Key: "d", Text: `{"old":"only old"}`},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := views.AuditDetails(tt.details); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("AuditDetails = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestAuditFilterFormPaths(t *testing.T) {
	tests := []struct {
		name string
		f    views.AuditFilterForm
		page int
		want string
	}{
		{"nothing", views.AuditFilterForm{}, 1, "/admin/audit"},
		{"page 0 is page 1", views.AuditFilterForm{}, 0, "/admin/audit"},
		{"a page", views.AuditFilterForm{}, 3, "/admin/audit?page=3"},
		{
			"every filter and a page, in the order of their names",
			views.AuditFilterForm{Room: 4, Actor: 2, Entity: "member", Action: "member.removed", From: "2026-10-01", To: "2026-10-31"},
			2, "/admin/audit?action=member.removed&actor=2&entity=member&from=2026-10-01&page=2&room=4&to=2026-10-31",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.f.AuditPath(tt.page); got != tt.want {
				t.Errorf("AuditPath = %q, want %q", got, tt.want)
			}
		})
	}
	if got := (views.AuditFilterForm{Actor: 2}).Query(1); got != "actor=2" {
		t.Errorf("Query = %q", got)
	}
}

func TestAdminTabs(t *testing.T) {
	for _, current := range []string{"users", "rooms", "audit"} {
		html := render(t, views.AdminTabs(current))
		if n := strings.Count(html, `aria-current="page"`); n != 1 {
			t.Errorf("%s: %d current tabs, want 1:\n%s", current, n, html)
		}
		requireContains(t, html, []string{`href="/admin/users"`, `href="/admin/rooms"`, `href="/admin/audit"`, ">Audit log</a>"}, nil)
	}
}

func TestAdminRoomView(t *testing.T) {
	room := app.Room{ID: 5, Name: "<b>den</b>", Visibility: app.VisibilityPrivate, MemberCount: 1}
	html := render(t, views.AdminRoom(views.AdminRoomRow{Room: room}))
	requireContains(t, html,
		[]string{`id="room-5"`, `&lt;b&gt;den&lt;/b&gt;`, "Private", "1 member", "Loading the members…", `/admin/rooms/5/members`},
		[]string{"<b>den</b>", "Remove</button>", "created by"})
}
