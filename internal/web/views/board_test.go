package views_test

import (
	"strings"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/web/views"
)

// testBoard is a board of three columns: a late card with two assignees, a card
// in the middle, and a finished card with a due date in the past.
func testBoard(canEdit bool) app.BoardView {
	past := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	future := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	return app.BoardView{
		Room:    app.Room{ID: 4, Name: "general", Visibility: app.VisibilityVisible, IsMember: canEdit},
		CanEdit: canEdit,
		Columns: []app.ColumnView{
			{ID: 10, Name: "To do", Position: 0, Tasks: []app.TaskCard{
				{
					ID: 31, Title: "Late <one>", PostID: 7, RootID: 7, ColumnID: 10, ColumnName: "To do", DueDate: &past, Overdue: true,
					Assignees: []app.UserRef{{ID: 1, Username: "alice", DisplayName: "Alice"}, {ID: 2, Username: "bob", DisplayName: "Bob <B>"}},
				},
				{ID: 32, Title: "Bare", PostID: 9, RootID: 7, ColumnID: 10, ColumnName: "To do", Position: 1},
			}},
			{ID: 11, Name: "In progress", Position: 1, Tasks: []app.TaskCard{
				{ID: 33, Title: "Soon", PostID: 7, RootID: 7, ColumnID: 11, ColumnName: "In progress", DueDate: &future},
			}},
			{ID: 12, Name: "Done", Position: 2, Last: true, Tasks: []app.TaskCard{
				{ID: 34, Title: "Finished", PostID: 8, RootID: 8, ColumnID: 12, ColumnName: "Done", Done: true, DueDate: &past},
			}},
		},
	}
}

func TestBoardColumnsOfAMember(t *testing.T) {
	html := render(t, views.BoardColumns(views.BoardData{Board: testBoard(true)}))
	requireContains(t, html, []string{
		`<div id="board-columns" class="board-columns">`,
		// Columns in order, the last one marked as done, with the counts.
		`<section id="column-10" class="board-column" data-column="10" aria-label="To do">`,
		`<section id="column-12" class="board-column board-column-done" data-column="12" aria-label="Done">`,
		`<span class="column-count" title="2 cards">2</span>`, `<span class="column-count" title="1 card">1</span>`,
		// Inline rename, the buttons that move and delete a column.
		`<form class="column-rename" method="post" action="/rooms/4/board/columns/10/rename"`, `<input class="column-name" name="name" type="text" value="To do" maxlength="40"`,
		`action="/rooms/4/board/columns/11/move"`, `<input type="hidden" name="position" value="0">`, `<input type="hidden" name="position" value="2">`,
		`aria-label="Move the column left" disabled>Move left</button>`, `aria-label="Move the column right" disabled>Move right</button>`,
		`<summary class="icon-button" aria-label="Column actions" title="Column actions">`, `data-preserve-attr="open"`,
		`aria-label="Rename the column"`,
		`action="/rooms/4/board/columns/12/delete"`, `confirm(&#39;Delete this column?&#39;) &amp;&amp; @post(&#39;/rooms/4/board/columns/12/delete&#39;`,
		// A card: id, drag, escaped title, assignees, overdue date, link to the post.
		`<li id="task-31" class="board-card" data-task="31" data-column="10" draggable="true">`,
		`<p class="card-title">Late &lt;one&gt;</p>`, `<p class="card-assignees" title="Assigned to">`, `<span class="card-assignee" title="Alice">`, `<span class="card-assignee" title="Bob &lt;B&gt;">`,
		`<span class="visually-hidden">Alice, Bob &lt;B&gt;</span>`,
		`<time class="task-due overdue" datetime="2026-10-20" title="Overdue">2026-10-20</time>`,
		`<a class="card-post" href="/posts/7#post-7">Open post</a>`, `<a class="card-post" href="/posts/7#post-9">Open post</a>`,
		// The link that opens the card form, by Datastar or by address.
		`<a class="card-details" href="/rooms/4/board/tasks/31/edit" data-on:click__prevent="@get(&#39;/rooms/4/board/tasks/31/edit&#39;)">Details</a>`,
		// The Move to… fallback: the other columns, not its own.
		`<form class="card-move" method="post" action="/rooms/4/board/tasks/31/move"`, `<option value="">Move to…</option>`,
		`<option value="11">In progress</option>`, `<option value="12">Done</option>`,
		// The finished card is done and its date is not red.
		`class="board-card board-card-done"`, `datetime="2026-10-20" title="Due date"`,
	}, []string{"Late <one>", "Bob <B>"})
	// Nothing is a form that a live update could take a text away from.
	for _, id := range []string{"board-add-form", "board-card-form", "board-editor"} {
		if strings.Contains(html, `id="`+id+`"`) {
			t.Errorf("the columns hold %s", id)
		}
	}
	// A card has no option for the column it is in.
	card := html[strings.Index(html, `id="task-31"`):]
	card = card[:strings.Index(card, "</li>")]
	if strings.Contains(card, `<option value="10">`) {
		t.Errorf("the card offers its own column:\n%s", card)
	}
	if n := strings.Count(html, "task-due overdue"); n != 1 {
		t.Errorf("%d overdue dates, want 1", n)
	}
}

// TestBoardCardMetaAndColumnMenu: a card shows its due date with the calendar
// icon and its assignees as avatars with their names on tooltips (the names for
// a screen reader are in the text), and the actions of a column are in its
// "more" menu, a details element that a live update leaves open.
func TestBoardCardMetaAndColumnMenu(t *testing.T) {
	html := render(t, views.BoardColumns(views.BoardData{Board: testBoard(true)}))
	card := html[strings.Index(html, `id="task-31"`):]
	card = card[:strings.Index(card, "</li>")]
	requireContains(t, card, []string{
		`<div class="card-meta"><p class="card-due"><svg class="icon"`, `<time class="task-due overdue" datetime="2026-10-20"`,
		`<span class="card-avatars"><span class="card-assignee" title="Alice"><span class="avatar avatar-sm`,
		`<span class="visually-hidden">Alice, Bob &lt;B&gt;</span>`,
	}, nil)
	// The card without a date or assignee has no meta row.
	bare := html[strings.Index(html, `id="task-32"`):]
	bare = bare[:strings.Index(bare, "</li>")]
	requireContains(t, bare, []string{`<p class="card-title">Bare</p>`}, []string{"card-meta", "card-due", "card-assignees"})
	// The menu of the first column: its three actions are inside it.
	column := html[strings.Index(html, `id="column-10"`):strings.Index(html, `id="column-11"`)]
	menu := column[strings.Index(column, `<details class="post-menu column-menu"`):strings.Index(column, `</details>`)]
	requireContains(t, menu, []string{
		`data-preserve-attr="open"`, `data-on:click__outside="el.removeAttribute('open')"`, `<div class="post-menu-panel column-actions">`,
		`/columns/10/move`, `/columns/10/delete`, `Move left`, `Move right`, `Delete column`,
	}, nil)
	if strings.Count(menu, "<form") != 3 {
		t.Errorf("the menu has %d forms, want 3:\n%s", strings.Count(menu, "<form"), menu)
	}
}

// TestBoardHeaderHasTheRoomHeaderOfTheFeed: the board page shows the name, the
// pill, the description with the member count and the tabs with Board current.
func TestBoardHeaderHasTheRoomHeaderOfTheFeed(t *testing.T) {
	b := testBoard(true)
	b.Room.Description = "Everything <here>"
	b.Room.MemberCount = 7
	html := render(t, views.BoardHeader(views.BoardData{Board: b}))
	requireContains(t, html, []string{
		`<header id="board-header" class="room-header board-header">`, "<h1>general</h1>", `<p class="room-description">Everything &lt;here&gt;</p>`,
		`<p class="hint room-members-count">7 members</p>`, `<a href="/rooms/4/board" aria-current="page">Board</a>`,
	}, []string{"board-title", "Everything <here>"})
}

func TestBoardColumnsRead(t *testing.T) {
	html := render(t, views.BoardColumns(views.BoardData{Board: testBoard(false)}))
	requireContains(t, html, []string{
		`<h2 class="column-name">To do</h2>`, `<p class="card-title">Late &lt;one&gt;</p>`, `<a class="card-post" href="/posts/7#post-7">Open post</a>`,
		`<li id="task-31" class="board-card" data-task="31" data-column="10">`, `class="task-due overdue"`,
	}, []string{"draggable", "card-move", "card-details", "column-rename", "column-actions", "Delete", "<form"})
}

func TestBoardPageVariants(t *testing.T) {
	user := app.User{DisplayName: "Alice", Role: app.RoleMember}
	sidebar := views.SidebarData{User: user, Rooms: []app.Room{{ID: 4, Name: "general"}}, CurrentRoomID: 4}
	editor := views.NewBoardEditorData(testBoard(true).Columns[0].Tasks[0], []app.Member{{UserID: 1, Username: "alice", DisplayName: "Alice"}})
	tests := []struct {
		name    string
		data    views.BoardData
		want    []string
		notWant []string
	}{
		{
			name: "a member",
			data: views.BoardData{Sidebar: sidebar, Board: testBoard(true)},
			want: []string{
				"<title id=\"page-title\">Board of general · Clowder</title>", `href="/rooms/4" class="current" data-room="general" aria-current="page"`,
				`<div id="live-stream" hidden data-init="@get(&#39;/rooms/4/board/stream?v=`,
				`<header id="board-header" class="room-header board-header">`, "<h1>general</h1>", `<span class="badge badge-visible">Visible</span>`,
				`<nav class="room-nav" aria-label="Room pages"><a href="/rooms/4">Posts</a> <a href="/rooms/4/board" aria-current="page">Board</a></nav>`,
				`<div id="board-note"></div>`, `<div id="board-message" class="board-message"></div>`, `<div id="board-editor" class="board-editor"></div>`,
				// The columns and the add form share a row, the form outside the columns.
				`<div class="board-scroll"><div id="board-columns" class="board-columns">`,
				`<div id="board-add" class="board-add"><form id="board-add-form" class="board-add-form" method="post" action="/rooms/4/board/columns"`,
				`<label for="board-add-name">New column</label>`,
				// The hidden form and link the script uses; the script.
				`<form id="board-drop" hidden method="post" action="/rooms/4/board/tasks/0/move" data-on:submit="@post(el.getAttribute(&#39;action&#39;), {contentType: &#39;form&#39;})">`,
				`<input type="hidden" name="column" value=""> <input type="hidden" name="position" value="0">`,
				`<a id="board-refresh" hidden href="/rooms/4/board" data-on:click__prevent="@get(&#39;/rooms/4/board/columns&#39;)">Refresh</a>`,
				`<script src="/static/board.js" defer></script>`,
			},
			notWant: []string{"Join this room"},
		},
		{
			name: "a person who has not joined",
			data: views.BoardData{Sidebar: sidebar, Board: testBoard(false)},
			want: []string{
				`<div id="board-note"><p class="hint join-note">Join this room to change the board</p></div>`,
				`<div id="board-add" class="board-add"></div>`, `<p class="card-title">Late &lt;one&gt;</p>`,
			},
			notWant: []string{"draggable", "board-add-form", "card-move", "column-rename"},
		},
		{
			name: "a message at the top",
			data: views.BoardData{Sidebar: sidebar, Board: testBoard(true), Message: "Move its cards first."},
			want: []string{`<div id="board-message" class="board-message"><p class="form-error" role="alert">Move its cards first.</p></div>`},
		},
		{
			name: "a card form is open in its own panel, outside the columns",
			data: views.BoardData{Sidebar: sidebar, Board: testBoard(true), Editor: &editor},
			want: []string{`<div id="board-editor" class="board-editor"><form id="board-card-form"`},
		},
		{
			name: "a private room",
			data: views.BoardData{Sidebar: sidebar, Board: func() app.BoardView {
				b := testBoard(true)
				b.Room.Visibility = app.VisibilityPrivate
				return b
			}()},
			want: []string{`class="badge badge-private"`, "Private"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			html := render(t, views.BoardPage(tt.data))
			requireContains(t, html, tt.want, tt.notWant)
			// The panel and the message are outside the columns whatever the state.
			if panel, columns := strings.Index(html, `id="board-editor"`), strings.Index(html, `id="board-columns"`); panel > columns {
				t.Errorf("the card panel (%d) is after the columns (%d)", panel, columns)
			}
		})
	}
}

func TestBoardEditor(t *testing.T) {
	due := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	task := app.TaskCard{
		ID: 31, Title: "Write <it>", DueDate: &due,
		Assignees: []app.UserRef{{ID: 2, Username: "bob", DisplayName: "Bob"}, {ID: 9, Username: "gone", DisplayName: "Gus"}},
	}
	members := []app.Member{{UserID: 1, Username: "alice", DisplayName: "Alice <A>"}, {UserID: 2, Username: "bob", DisplayName: "Bob"}}
	typed := views.NewBoardEditorData(task, members)
	typed.Title, typed.Assignees, typed.Due, typed.ClearDue = "", []uint64{1}, "next week", true
	typed.Errors = views.FormErrors{Form: "Try again.", Fields: map[string]string{"title": "Write a title of 1 to 200 characters.", "assignees": "Assign only members of the room.", "dueDate": "Write the due date as YYYY-MM-DD."}}
	tests := []struct {
		name    string
		data    views.BoardEditorData
		want    []string
		notWant []string
	}{
		{
			name: "filled with the card",
			data: views.NewBoardEditorData(task, members),
			want: []string{
				`<form id="board-card-form" class="board-card-form" method="post" action="/rooms/4/board/tasks/31"`,
				`data-on:submit="@post(&#39;/rooms/4/board/tasks/31&#39;, {contentType: &#39;form&#39;})"`,
				`id="card-title-31" name="title" type="text" value="Write &lt;it&gt;" maxlength="200"`,
				`<select id="card-assignees-31" name="assignees" multiple size="4">`,
				`<option value="1">Alice &lt;A&gt; (@alice)</option>`, `<option value="2" selected>Bob (@bob)</option>`,
				// A former member stays, selected and marked.
				`<option value="9" selected>Gus (@gone, left the room)</option>`,
				`id="card-due-31" name="due" type="date" value="2026-11-05"`, `<input id="card-clear-due-31" name="clear_due" type="checkbox" value="1">`,
				"Remove the due date", `<button type="submit">Save</button>`,
				`<a class="button-link" href="/rooms/4/board" data-on:click__prevent="document.getElementById(&#39;board-editor&#39;).replaceChildren()">Cancel</a>`,
				`data-init="el.scrollIntoView(`,
			},
			notWant: []string{`class="field-error"`, `aria-invalid`, "checked", "Write <it>"},
		},
		{
			name: "what was typed, with the problems",
			data: typed,
			want: []string{
				`<p class="form-error" role="alert">Try again.</p>`,
				`id="card-title-31" name="title" type="text" value="" maxlength="200" autocomplete="off" aria-invalid="true" aria-describedby="card-title-31-error"`,
				`<p class="field-error" id="card-title-31-error" role="alert">Write a title of 1 to 200 characters.</p>`,
				`<p class="field-error" role="alert">Assign only members of the room.</p>`,
				`<option value="1" selected>Alice &lt;A&gt; (@alice)</option>`, `<option value="2">Bob (@bob)</option>`,
				`value="next week"`, `aria-describedby="card-due-31-error"`, `<p class="field-error" id="card-due-31-error" role="alert">Write the due date as YYYY-MM-DD.</p>`,
				`name="clear_due" type="checkbox" value="1" checked`,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			html := render(t, views.BoardEditor(views.BoardData{Board: testBoard(true), Editor: &tt.data}))
			requireContains(t, html, append(tt.want, `<div id="board-editor" class="board-editor">`), tt.notWant)
		})
	}
	// Closed, the panel is empty.
	if html := render(t, views.BoardEditor(views.BoardData{Board: testBoard(true)})); html != `<div id="board-editor" class="board-editor"></div>` {
		t.Errorf("closed panel = %s", html)
	}
}

func TestBoardPathsAndVersion(t *testing.T) {
	if got := views.BoardPath(4); got != "/rooms/4/board" {
		t.Errorf("BoardPath = %q", got)
	}
	if got := views.BoardColumnsPath(4); got != "/rooms/4/board/columns" {
		t.Errorf("BoardColumnsPath = %q", got)
	}
	if got := views.BoardStreamPath(4, "abc"); got != "/rooms/4/board/stream?v=abc" {
		t.Errorf("BoardStreamPath = %q", got)
	}
	base := views.BoardVersion(testBoard(true))
	if again := views.BoardVersion(testBoard(true)); again != base {
		t.Errorf("version of the same board = %q and %q", base, again)
	}
	changes := map[string]func(b *app.BoardView){
		"a card moves": func(b *app.BoardView) {
			b.Columns[0].Tasks, b.Columns[1].Tasks = b.Columns[0].Tasks[1:], append(b.Columns[1].Tasks, b.Columns[0].Tasks[0])
		},
		"a card is renamed":  func(b *app.BoardView) { b.Columns[0].Tasks[1].Title = "Other" },
		"a card is assigned": func(b *app.BoardView) { b.Columns[0].Tasks[1].Assignees = []app.UserRef{{ID: 5}} },
		"a due date is set": func(b *app.BoardView) {
			d := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
			b.Columns[0].Tasks[1].DueDate = &d
		},
		"a card is overdue":   func(b *app.BoardView) { b.Columns[1].Tasks[0].Overdue = true },
		"a column is renamed": func(b *app.BoardView) { b.Columns[2].Name = "Finished" },
		"a column goes":       func(b *app.BoardView) { b.Columns = b.Columns[:2] },
		"the viewer joins":    func(b *app.BoardView) { b.CanEdit = false },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			b := testBoard(true)
			change(&b)
			if got := views.BoardVersion(b); got == base {
				t.Errorf("version did not change when %s", name)
			}
		})
	}
}

func TestRoomNav(t *testing.T) {
	tests := []struct {
		name    string
		current views.RoomNavCurrent
		want    string
	}{
		{"posts", views.NavPosts, `<a href="/rooms/4" aria-current="page">Posts</a> <a href="/rooms/4/board">Board</a>`},
		{"board", views.NavBoard, `<a href="/rooms/4">Posts</a> <a href="/rooms/4/board" aria-current="page">Board</a>`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requireContains(t, render(t, views.RoomNav(4, tt.current)), []string{`<nav class="room-nav" aria-label="Room pages">` + tt.want + `</nav>`}, nil)
		})
	}
	// The header of the room page has it, with the posts as current.
	header := render(t, views.RoomHeader(views.RoomData{Room: app.Room{ID: 9, Name: "general", Visibility: app.VisibilityVisible}}))
	requireContains(t, header, []string{`<a href="/rooms/9" aria-current="page">Posts</a> <a href="/rooms/9/board">Board</a>`}, nil)
}

func TestBoardMessageAndNote(t *testing.T) {
	requireContains(t, render(t, views.BoardMessage("")), []string{`<div id="board-message" class="board-message"></div>`}, nil)
	requireContains(t, render(t, views.BoardMessage("A <b>")), []string{`role="alert">A &lt;b&gt;</p>`}, nil)
	requireContains(t, render(t, views.BoardNote(views.BoardData{Board: testBoard(true)})), []string{`<div id="board-note"></div>`}, nil)
	requireContains(t, render(t, views.BoardNote(views.BoardData{Board: testBoard(false)})), []string{"Join this room to change the board"}, nil)
	requireContains(t, render(t, views.BoardAddColumn(views.BoardData{Board: testBoard(false)})), []string{`<div id="board-add" class="board-add"></div>`}, nil)
}
