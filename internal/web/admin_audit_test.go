package web

import (
	"net/http"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/store"
)

// auditEnv is a roomsEnv whose log has, oldest first, on 2026-10-26 09:30
// (the fake clock's start):
//
//	admin created general and random
//	alice, bob joined general
//	alice created secret
//	alice added Bob to #secret
//
// and, on 2026-10-27 09:30:
//
//	Bob left #general
//	Admin removed Bob from #secret
//	Admin renamed #general to #lobby
//	Admin deleted #random
type auditEnv struct {
	*roomsEnv
}

func newAuditEnv(t *testing.T) *auditEnv {
	t.Helper()
	e := &auditEnv{newRoomsEnv(t)}
	e.clk.Set(time.Date(2026, 10, 27, 9, 30, 0, 0, time.UTC))
	ctx := t.Context()
	if err := e.rooms.Leave(ctx, e.actor(e.bob), e.general.ID); err != nil {
		t.Fatalf("Leave: %+v", err)
	}
	if err := e.rooms.RemoveMember(ctx, e.actor(e.admin), e.secret.ID, e.bob.ID); err != nil {
		t.Fatalf("RemoveMember: %+v", err)
	}
	if err := e.rooms.Update(ctx, e.actor(e.admin), e.general.ID, app.RoomInput{Name: "lobby", Description: "Everything"}); err != nil {
		t.Fatalf("Update: %+v", err)
	}
	if err := e.rooms.Delete(ctx, e.actor(e.admin), e.random.ID); err != nil {
		t.Fatalf("Delete: %+v", err)
	}
	return e
}

// summaries returns the summaries of the events of the page, in the order of
// the table.
func summaries(t *testing.T, body string) []string {
	t.Helper()
	re := regexp.MustCompile(`<td class="audit-summary">\s*<span>(.*?)</span>`)
	var out []string
	for _, m := range re.FindAllStringSubmatch(body, -1) {
		out = append(out, m[1])
	}
	return out
}

// auditPage gets the audit log page as the admin and returns the summaries.
func (e *auditEnv) auditPage(t *testing.T, query string) []string {
	t.Helper()
	rec := e.send(get("/admin/audit"+query, e.cookies["admin"]))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /admin/audit%s: status = %d, want 200; body:\n%s", query, rec.Code, rec.Body)
	}
	return summaries(t, rec.Body.String())
}

// insertEvent writes an audit event straight into the store, for the shapes
// that no service writes yet.
func (e *auditEnv) insertEvent(t *testing.T, ev store.AuditEvent) {
	t.Helper()
	if err := e.st.CreateAuditEvent(t.Context(), &ev); err != nil {
		t.Fatalf("CreateAuditEvent: %+v", err)
	}
}

func TestAdminAuditPage(t *testing.T) {
	t.Parallel()
	e := newAuditEnv(t)
	rec := e.send(get("/admin/audit", e.cookies["admin"]))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	requireBody(t, rec, []string{
		"<title>Audit log · Clowder</title>", `<h1>Audit log</h1>`, `id="audit-panel"`, `<table class="audit">`,
		`class="current" aria-current="page">Audit log</a>`, `href="/admin/users"`, `href="/admin/rooms"`,
		`id="sidebar-user">Admin</p>`, `id="logout-form"`,
		"Time (UTC)", "Actor", "Room", "Action", "Summary",
		// Times are UTC, to the minute.
		"2026-10-26 09:30", "2026-10-27 09:30",
		// Actors with the display name and the username.
		`Alice <span class="member-username">@alice</span>`, `Admin <span class="member-username">@admin</span>`,
		`<code>member.removed</code>`, `<code>room.created</code>`,
	}, "No events match.", `class="newer"`, `class="older"`)

	got := summaries(t, rec.Body.String())
	want := []string{
		// Newest first.
		"Admin deleted #random",
		"Admin renamed #general to #lobby",
		"Admin removed Bob from #secret",
		"Bob left #general",
		"Alice added Bob to #secret",
		"Alice created #secret",
		"Bob joined #general",
		"Alice joined #general",
		"Admin created #random",
		"Admin created #general",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("summaries:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestAdminAuditRoomColumn(t *testing.T) {
	t.Parallel()
	e := newAuditEnv(t)
	rec := e.send(get("/admin/audit", e.cookies["admin"]))
	body := rec.Body.String()
	row := func(summary string) string {
		t.Helper()
		re := regexp.MustCompile(`(?s)<tr id="event-\d+".*?</tr>`)
		for _, r := range re.FindAllString(body, -1) {
			if strings.Contains(r, "<span>"+summary+"</span>") {
				return r
			}
		}
		t.Fatalf("no row for %q", summary)
		return ""
	}
	t.Run("a room that exists is a link to its row", func(t *testing.T) {
		// The private room, which the admin is not a member of.
		r := row("Admin removed Bob from #secret")
		want := `<a href="/admin/rooms#room-` + idText(e.secret.ID) + `">#secret</a>`
		if !strings.Contains(r, want) {
			t.Errorf("row lacks %q:\n%s", want, r)
		}
	})
	t.Run("a renamed room shows its current name", func(t *testing.T) {
		r := row("Alice joined #general")
		want := `<a href="/admin/rooms#room-` + idText(e.general.ID) + `">#lobby</a>`
		if !strings.Contains(r, want) {
			t.Errorf("row lacks %q:\n%s", want, r)
		}
	})
	t.Run("a deleted room is plain text with the name of the event", func(t *testing.T) {
		for _, summary := range []string{"Admin deleted #random", "Admin created #random"} {
			r := row(summary)
			if !strings.Contains(r, `<span class="deleted-room">#random <span class="hint">(deleted)</span></span>`) {
				t.Errorf("%s: the deleted room is not plain text:\n%s", summary, r)
			}
			if strings.Contains(r, "/admin/rooms#room-"+idText(e.random.ID)) || strings.Contains(r, "<a href") {
				t.Errorf("%s: the deleted room is a link:\n%s", summary, r)
			}
		}
	})
	t.Run("an event of a room that is gone, with no name in the details", func(t *testing.T) {
		// The name comes from the summary.
		if err := e.rooms.Update(t.Context(), e.actor(e.admin), e.secret.ID, app.RoomInput{Name: "secret"}); err != nil {
			t.Fatal(err)
		}
		if err := e.rooms.Delete(t.Context(), e.actor(e.admin), e.secret.ID); err != nil {
			t.Fatal(err)
		}
		body = e.send(get("/admin/audit", e.cookies["admin"])).Body.String()
		r := row("Admin removed Bob from #secret")
		if !strings.Contains(r, `<span class="deleted-room">#secret <span class="hint">(deleted)</span></span>`) {
			t.Errorf("row:\n%s", r)
		}
	})
	t.Run("a room the event does not name", func(t *testing.T) {
		e.insertEvent(t, store.AuditEvent{
			ActorID: e.admin.ID, Action: app.ActionPostCreated, RoomID: 4242, EntityType: app.EntityPost, EntityID: 1,
			Summary: "something happened", Details: `{}`,
		})
		body = e.send(get("/admin/audit", e.cookies["admin"])).Body.String()
		r := row("something happened")
		if !strings.Contains(r, `Room 4242 <span class="hint">(deleted)</span>`) {
			t.Errorf("row:\n%s", r)
		}
	})
}

func TestAdminAuditDetails(t *testing.T) {
	t.Parallel()
	e := newAuditEnv(t)
	body := e.send(get("/admin/audit", e.cookies["admin"])).Body.String()
	for _, want := range []string{
		`<details class="audit-details">`, `<summary>Details</summary>`, `<table class="audit-details-table">`,
		// room.renamed: the name changed, the description did not so it is not there.
		`<th scope="row">name</th>`, `<td>general</td>`, `<td>lobby</td>`,
		// room.created: nothing before.
		`<td>—</td>`,
		// room.deleted: nothing after.
		// member events: the user, spanning both columns.
		`<th scope="row">username</th>`, `<td colspan="2">bob</td>`, `<th scope="row">userID</th>`, `<td colspan="2">` + idText(e.bob.ID) + `</td>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q", want)
		}
	}
	// The description of a room without one is shown as empty.
	if !strings.Contains(body, "(empty)") {
		t.Error("an empty description is not shown as (empty)")
	}
}

func TestAdminAuditBodyChangeShape(t *testing.T) {
	t.Parallel()
	// No service writes post events yet: the shape of SPEC §7.10 is inserted
	// by hand. Only that the body changed and how long it is is shown, never
	// text.
	e := newAuditEnv(t)
	e.insertEvent(t, store.AuditEvent{
		ActorID: e.alice.ID, Action: app.ActionPostEdited, RoomID: e.secret.ID, EntityType: app.EntityPost, EntityID: 7,
		Summary: "Alice edited a post in #secret", Details: `{"bodyChanged":true,"length":42}`,
	})
	e.insertEvent(t, store.AuditEvent{
		ActorID: e.alice.ID, Action: app.ActionPostEdited, RoomID: e.secret.ID, EntityType: app.EntityComment, EntityID: 8,
		Summary: "Alice edited a comment in #secret", Details: `{"bodyChanged":true,"length":1}`,
	})
	e.insertEvent(t, store.AuditEvent{
		ActorID: e.alice.ID, Action: app.ActionTaskEdited, RoomID: e.secret.ID, EntityType: app.EntityTask, EntityID: 9,
		Summary: "Alice changed task Write", Details: `{"title":{"old":"Write","new":"Write it"},"due":{"old":null,"new":"2026-11-01"},"count":3,"done":false}`,
	})
	body := e.send(get("/admin/audit", e.cookies["admin"])).Body.String()
	for _, want := range []string{
		`<th scope="row">body</th>`, `<td colspan="2">body changed (42 characters)</td>`,
		`<td colspan="2">body changed (1 character)</td>`,
		// Old and new values, null as a dash, numbers and booleans as text.
		`<th scope="row">title</th>`, `<td>Write</td>`, `<td>Write it</td>`,
		`<th scope="row">due</th>`, `<td>2026-11-01</td>`,
		`<th scope="row">count</th>`, `<td colspan="2">3</td>`, `<td colspan="2">false</td>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q", want)
		}
	}
	// The raw keys of the body shape are not rows of their own.
	for _, notWant := range []string{`<th scope="row">bodyChanged</th>`, `<th scope="row">length</th>`} {
		if strings.Contains(body, notWant) {
			t.Errorf("body has %q", notWant)
		}
	}
}

func TestAdminAuditFilters(t *testing.T) {
	t.Parallel()
	e := newAuditEnv(t)
	id := idText
	tests := []struct {
		name  string
		query string
		want  []string
	}{
		{
			name: "no filter is the whole log", query: "",
			want: []string{
				"Admin deleted #random", "Admin renamed #general to #lobby", "Admin removed Bob from #secret", "Bob left #general",
				"Alice added Bob to #secret", "Alice created #secret", "Bob joined #general", "Alice joined #general",
				"Admin created #random", "Admin created #general",
			},
		},
		{
			name: "empty values do not filter, as the form sends them", query: "?room=&actor=&entity=&action=&from=&to=",
			want: []string{
				"Admin deleted #random", "Admin renamed #general to #lobby", "Admin removed Bob from #secret", "Bob left #general",
				"Alice added Bob to #secret", "Alice created #secret", "Bob joined #general", "Alice joined #general",
				"Admin created #random", "Admin created #general",
			},
		},
		{
			name: "a private room the admin is not in", query: "?room=" + id(e.secret.ID),
			want: []string{"Admin removed Bob from #secret", "Alice added Bob to #secret", "Alice created #secret"},
		},
		{
			name: "a room that is gone", query: "?room=" + id(e.random.ID),
			want: []string{"Admin deleted #random", "Admin created #random"},
		},
		{
			name: "an actor", query: "?actor=" + id(e.alice.ID),
			want: []string{"Alice added Bob to #secret", "Alice created #secret", "Alice joined #general"},
		},
		{
			name: "an actor with no events", query: "?actor=" + id(e.carol.ID),
		},
		{
			name: "an entity type", query: "?entity=room",
			want: []string{"Admin deleted #random", "Admin renamed #general to #lobby", "Alice created #secret", "Admin created #random", "Admin created #general"},
		},
		{
			name: "an action", query: "?action=member.left",
			want: []string{"Bob left #general"},
		},
		{
			name: "an entity type with no events", query: "?entity=post",
		},
		{
			name: "from: the start of the day", query: "?from=2026-10-27",
			want: []string{"Admin deleted #random", "Admin renamed #general to #lobby", "Admin removed Bob from #secret", "Bob left #general"},
		},
		{
			name: "to: through the end of the day", query: "?to=2026-10-26",
			want: []string{
				"Alice added Bob to #secret", "Alice created #secret", "Bob joined #general", "Alice joined #general",
				"Admin created #random", "Admin created #general",
			},
		},
		{
			name: "a range of one day", query: "?from=2026-10-27&to=2026-10-27",
			want: []string{"Admin deleted #random", "Admin renamed #general to #lobby", "Admin removed Bob from #secret", "Bob left #general"},
		},
		{
			name: "a range before the log", query: "?from=2026-10-01&to=2026-10-25",
		},
		{
			name: "a range after the log", query: "?from=2026-10-28",
		},
		{
			name: "a range that is backwards", query: "?from=2026-10-27&to=2026-10-26",
		},
		{
			name: "all of them together", query: "?room=" + id(e.secret.ID) + "&actor=" + id(e.admin.ID) + "&entity=member&action=member.removed&from=2026-10-27&to=2026-10-27",
			want: []string{"Admin removed Bob from #secret"},
		},
		{
			name: "filters that do not match together", query: "?room=" + id(e.secret.ID) + "&actor=" + id(e.carol.ID),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := e.auditPage(t, tt.query)
			if len(got) == 0 && len(tt.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("summaries:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(tt.want, "\n"))
			}
		})
	}
}

func TestAdminAuditNoMatch(t *testing.T) {
	t.Parallel()
	e := newAuditEnv(t)
	rec := e.send(get("/admin/audit?actor="+idText(e.carol.ID), e.cookies["admin"]))
	requireBody(t, rec, []string{"No events match.", `id="audit-panel"`}, `<table class="audit">`, "form-error")
}

func TestAdminAuditDateRangeEdges(t *testing.T) {
	t.Parallel()
	// Days are UTC days: From is the start of its day and To the start of the
	// day after. Events at the last second of a day and the first of the next.
	e := newAuditEnv(t)
	for _, ev := range []struct {
		at      time.Time
		summary string
	}{
		{time.Date(2026, 11, 1, 23, 59, 59, 0, time.UTC), "last second of 11-01"},
		{time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC), "first second of 11-02"},
		{time.Date(2026, 11, 2, 23, 59, 59, 0, time.UTC), "last second of 11-02"},
		{time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC), "first second of 11-03"},
	} {
		e.clk.Set(ev.at)
		e.insertEvent(t, store.AuditEvent{
			ActorID: e.admin.ID, Action: app.ActionRoomCreated, RoomID: e.general.ID, EntityType: app.EntityRoom, EntityID: e.general.ID,
			Summary: ev.summary, Details: `{}`,
		})
	}
	tests := []struct {
		query string
		want  []string
	}{
		{"?from=2026-11-02&to=2026-11-02", []string{"last second of 11-02", "first second of 11-02"}},
		{"?from=2026-11-02", []string{"first second of 11-03", "last second of 11-02", "first second of 11-02"}},
		{"?to=2026-11-02", []string{"last second of 11-02", "first second of 11-02", "last second of 11-01"}},
		{"?from=2026-11-03&to=2026-11-03", []string{"first second of 11-03"}},
		{"?from=2026-11-01&to=2026-11-01", []string{"last second of 11-01"}},
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			got := e.auditPage(t, tt.query)
			// Keep the four events of this test, not the older ones.
			var mine []string
			for _, s := range got {
				if strings.Contains(s, "second of") {
					mine = append(mine, s)
				}
			}
			if !reflect.DeepEqual(mine, tt.want) {
				t.Errorf("events:\n%s\nwant:\n%s", strings.Join(mine, "\n"), strings.Join(tt.want, "\n"))
			}
		})
	}
	// The times are shown in UTC.
	body := e.send(get("/admin/audit?from=2026-11-02&to=2026-11-02", e.cookies["admin"])).Body.String()
	if !strings.Contains(body, "2026-11-02 23:59") || !strings.Contains(body, "2026-11-02 00:00") {
		t.Errorf("times of the edges are not shown in UTC:\n%s", body)
	}
}

func TestAdminAuditFilterForm(t *testing.T) {
	t.Parallel()
	e := newAuditEnv(t)
	t.Run("the options", func(t *testing.T) {
		rec := e.send(get("/admin/audit", e.cookies["admin"]))
		requireBody(t, rec, []string{
			`id="audit-filter"`, `method="get"`, `action="/admin/audit"`, `data-on:submit="@get('/admin/audit', {contentType: 'form'})"`,
			`<option value="" selected>Any room</option>`,
			// Every room, the private one of alice included; the deleted one is gone.
			`<option value="` + idText(e.secret.ID) + `">secret</option>`, `<option value="` + idText(e.general.ID) + `">lobby</option>`,
			`<option value="" selected>Anyone</option>`,
			`<option value="` + idText(e.carol.ID) + `">Carol (@carol)</option>`, `<option value="` + idText(e.admin.ID) + `">Admin (@admin)</option>`,
			`<option value="" selected>Any type</option>`, `<option value="member">member</option>`, `<option value="comment">comment</option>`,
			`<option value="" selected>Any action</option>`, `<option value="member.removed">member.removed</option>`, `<option value="task.moved">task.moved</option>`,
			`name="from" type="date"`, `name="to" type="date"`, `<button type="submit">Filter</button>`, `href="/admin/audit">Clear</a>`,
		}, "Deleted room")
	})
	t.Run("the filters are selected and kept", func(t *testing.T) {
		q := "?room=" + idText(e.secret.ID) + "&actor=" + idText(e.admin.ID) + "&entity=member&action=member.removed&from=2026-10-27&to=2026-10-28"
		rec := e.send(get("/admin/audit"+q, e.cookies["admin"]))
		requireBody(t, rec, []string{
			`<option value="` + idText(e.secret.ID) + `" selected>secret</option>`,
			`<option value="` + idText(e.admin.ID) + `" selected>Admin (@admin)</option>`,
			`<option value="member" selected>member</option>`, `<option value="member.removed" selected>member.removed</option>`,
			`name="from" type="date" value="2026-10-27"`, `name="to" type="date" value="2026-10-28"`,
		}, `<option value="" selected>`)
	})
	t.Run("a deleted room is offered by its ID so that its events can be filtered", func(t *testing.T) {
		rec := e.send(get("/admin/audit?room="+idText(e.random.ID), e.cookies["admin"]))
		requireBody(t, rec, []string{`<option value="` + idText(e.random.ID) + `" selected>Deleted room (` + idText(e.random.ID) + `)</option>`})
	})
}

func TestAdminAuditInvalidFilters(t *testing.T) {
	t.Parallel()
	e := newAuditEnv(t)
	tests := []struct {
		name    string
		query   string
		message string
		count   int // events shown: invalid filters are left out
	}{
		{"room", "?room=abc", messageAuditRoom, 10},
		{"room zero", "?room=0", messageAuditRoom, 10},
		{"room negative", "?room=-1", messageAuditRoom, 10},
		{"actor", "?actor=x", messageAuditActor, 10},
		{"entity", "?entity=planet", messageAuditEntity, 10},
		{"action", "?action=member.exploded", messageAuditAction, 10},
		{"from", "?from=yesterday", messageAuditFrom, 10},
		{"from in another format", "?from=26/10/2026", messageAuditFrom, 10},
		{"to", "?to=2026-13-01", messageAuditTo, 10},
		{"the first problem is shown, the valid filters still apply", "?room=abc&actor=" + idText(e.carol.ID) + "&entity=planet", messageAuditRoom, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := e.send(get("/admin/audit"+tt.query, e.cookies["admin"]))
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			requireBody(t, rec, []string{`<p class="form-error" role="alert">` + tt.message + `</p>`})
			if got := len(summaries(t, rec.Body.String())); got != tt.count {
				t.Errorf("%d events, want %d", got, tt.count)
			}
		})
	}
	// A page that is not a number, zero or negative is the first page.
	for _, p := range []string{"abc", "0", "-3", "1"} {
		if got := e.auditPage(t, "?page="+p); len(got) != 10 {
			t.Errorf("page=%s: %d events, want the 10 of page 1", p, len(got))
		}
	}
}

// manyEvents inserts n events of room by actor.
func (e *auditEnv) manyEvents(t *testing.T, n int, actor app.User, room app.Room) {
	t.Helper()
	for i := range n {
		e.insertEvent(t, store.AuditEvent{
			ActorID: actor.ID, Action: app.ActionPostCreated, RoomID: room.ID, EntityType: app.EntityPost, EntityID: uint64(i + 1),
			Summary: "post " + strconv.Itoa(i+1), Details: `{}`,
		})
	}
}

func TestAdminAuditPagination(t *testing.T) {
	t.Parallel()
	e := newAuditEnv(t)
	// 10 events of the log + 120 posts of carol = 130: three pages of 50, 50, 30.
	e.manyEvents(t, 120, e.carol, e.secret)

	pageOf := func(query string) (string, []string) {
		t.Helper()
		rec := e.send(get("/admin/audit"+query, e.cookies["admin"]))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		return rec.Body.String(), summaries(t, rec.Body.String())
	}

	t.Run("the first page has 50 events and an Older link, no Newer", func(t *testing.T) {
		body, got := pageOf("")
		if len(got) != 50 || got[0] != "post 120" || got[49] != "post 71" {
			t.Errorf("page 1: %d events, first %q, last %q", len(got), got[0], got[len(got)-1])
		}
		if !strings.Contains(body, `class="older" href="/admin/audit?page=2"`) || strings.Contains(body, `class="newer"`) {
			t.Errorf("pager of page 1:\n%s", body)
		}
		if !strings.Contains(body, "Page 1") {
			t.Error("no page number")
		}
	})
	t.Run("the middle page has both links", func(t *testing.T) {
		body, got := pageOf("?page=2")
		if len(got) != 50 || got[0] != "post 70" || got[49] != "post 21" {
			t.Errorf("page 2: %d events, first %q", len(got), got[0])
		}
		// Newer goes to page 1, which is the URL without a page.
		if !strings.Contains(body, `class="newer" href="/admin/audit"`) || !strings.Contains(body, `class="older" href="/admin/audit?page=3"`) {
			t.Errorf("pager of page 2:\n%s", body)
		}
	})
	t.Run("the last page has no Older link", func(t *testing.T) {
		body, got := pageOf("?page=3")
		if len(got) != 30 || got[0] != "post 20" || got[29] != "Admin created #general" {
			t.Errorf("page 3: %d events, first %q, last %q", len(got), got[0], got[len(got)-1])
		}
		if !strings.Contains(body, `class="newer" href="/admin/audit?page=2"`) || strings.Contains(body, `class="older"`) {
			t.Errorf("pager of page 3:\n%s", body)
		}
	})
	t.Run("a page past the end is empty with a way back", func(t *testing.T) {
		body, got := pageOf("?page=9")
		if len(got) != 0 || !strings.Contains(body, "No events match.") || !strings.Contains(body, `class="newer" href="/admin/audit?page=8"`) {
			t.Errorf("page 9: %d events\n%s", len(got), body)
		}
		if _, got := pageOf("?page=99999999999999999999"); len(got) != 50 {
			t.Errorf("a page that overflows is page 1: %d events", len(got))
		}
	})
	t.Run("the links keep the filters", func(t *testing.T) {
		q := "?actor=" + idText(e.carol.ID) + "&room=" + idText(e.secret.ID) + "&entity=post&action=post.created&from=2026-10-26&to=2026-12-31"
		body, got := pageOf(q + "&page=2")
		if len(got) != 50 {
			t.Fatalf("%d events, want 50", len(got))
		}
		// The parameters are in the order of their names.
		const page = "&amp;page="
		before := "action=post.created&amp;actor=" + idText(e.carol.ID) + "&amp;entity=post&amp;from=2026-10-26"
		after := "&amp;room=" + idText(e.secret.ID) + "&amp;to=2026-12-31"
		for _, want := range []string{
			`class="newer" href="/admin/audit?` + before + after + `"`,
			`class="older" href="/admin/audit?` + before + page + "3" + after + `"`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("body lacks %q", want)
			}
		}
		// Page 3 of this filter has the 20 events left.
		if _, got := pageOf(q + "&page=3"); len(got) != 20 {
			t.Errorf("page 3: %d events, want 20", len(got))
		}
	})
}

func TestAdminAuditByDatastar(t *testing.T) {
	t.Parallel()
	e := newAuditEnv(t)
	q := "?actor=" + idText(e.alice.ID) + "&page=1&entity=room"
	rec := e.send(get("/admin/audit"+q, e.cookies["admin"], asDatastar()))
	requireSSE(t, rec)
	body := rec.Body.String()
	for _, want := range []string{
		// Only the table is patched, the form stays as it is.
		`id="audit-panel"`, `Alice created #secret`,
		// The address bar gets the same filters, in their canonical form.
		`window.history.replaceState({}, &#34;&#34;, &#34;/admin/audit?actor=` + idText(e.alice.ID) + `&amp;entity=room&#34;)`,
	} {
		if !strings.Contains(body, want) && !strings.Contains(body, strings.NewReplacer("&#34;", `"`, "&amp;", "&").Replace(want)) {
			t.Errorf("body lacks %q:\n%s", want, body)
		}
	}
	for _, notWant := range []string{`id="audit-filter"`, "<aside", "Admin created #general", "Alice joined #general", "<html"} {
		if strings.Contains(body, notWant) {
			t.Errorf("body has %q:\n%s", notWant, body)
		}
	}

	// The page of a plain GET with the same query has the same events.
	plain := e.auditPage(t, q)
	if !reflect.DeepEqual(plain, []string{"Alice created #secret"}) {
		t.Errorf("plain GET = %v", plain)
	}

	// Page 2 of a Datastar request sets the page in the address.
	e.manyEvents(t, 60, e.carol, e.secret)
	rec = e.send(get("/admin/audit?actor="+idText(e.carol.ID)+"&page=2", e.cookies["admin"], asDatastar()))
	requireSSE(t, rec)
	requireBody(t, rec, []string{"page=2", "post 10", `class="newer"`}, `class="older"`)
}

func TestAdminAuditIsInvisibleToMembers(t *testing.T) {
	t.Parallel()
	e := newAuditEnv(t)
	for _, tt := range []struct {
		name  string
		path  string
		opts  []reqOpt
		cased string
	}{
		{"page", "/admin/audit", nil, "alice"},
		{"page with filters", "/admin/audit?actor=" + idText(e.alice.ID) + "&room=" + idText(e.secret.ID), nil, "alice"},
		{"page by Datastar", "/admin/audit", []reqOpt{asDatastar()}, "alice"},
		{"page by a member of no room", "/admin/audit", nil, "carol"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rec := e.send(get(tt.path, append([]reqOpt{e.cookies[tt.cased]}, tt.opts...)...))
			if rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404 (not 403); body:\n%s", rec.Code, rec.Body)
			}
			requireBody(t, rec, []string{"Page not found"}, "Audit log", "audit-panel", "Admin removed Bob", "Forbidden", "not allowed")
		})
	}
	// There is no route that changes the log.
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		req := get("/admin/audit", e.cookies["admin"])
		req.Method = method
		if rec := e.send(req); rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s /admin/audit: status = %d, want 405", method, rec.Code)
		}
	}
}

func TestAdminAuditIgnoresUnknownParameters(t *testing.T) {
	t.Parallel()
	// The Datastar client adds a "datastar" parameter to a GET; others are
	// ignored too.
	e := newAuditEnv(t)
	got := e.auditPage(t, "?actor="+idText(e.alice.ID)+"&datastar=%7B%7D&other=1")
	if len(got) != 3 {
		t.Errorf("%d events, want alice's 3: %v", len(got), got)
	}
}
