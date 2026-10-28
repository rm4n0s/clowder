package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/auth"
	"github.com/rm4n0s/clowder/internal/clock"
	"github.com/rm4n0s/clowder/internal/config"
	"github.com/rm4n0s/clowder/internal/files"
	"github.com/rm4n0s/clowder/internal/markdown"
	"github.com/rm4n0s/clowder/internal/store"
	"github.com/rm4n0s/errors"
)

// demoPassword is the password of every demo user.
const demoPassword = "demo-password-1234"

// demoUsers are the users of the demo, in the order they are made.
var demoUsers = []struct {
	username    string
	displayName string
	role        store.Role
}{
	{"admin", "Admin", store.RoleAdmin},
	{"alice", "Alice", store.RoleMember},
	{"bob", "Bob", store.RoleMember},
	{"carol", "Carol", store.RoleMember},
	// Dave, Erin and Frank make #general large enough that @room asks before
	// it notifies more than 5 people (SPEC §7.15).
	{"dave", "Dave", store.RoleMember},
	{"erin", "Erin", store.RoleMember},
	{"frank", "Frank", store.RoleMember},
}

// runDemo prepares the data directory of the demo. The directory is made if it
// is missing and must otherwise be empty, so that demo never touches the data
// of a real instance. It then makes the database and the demo users in it,
// through the accounts service (an invitation and a sign-up for each), then
// the rooms through the rooms service, their posts and the comments under
// some of them through the posts service, and prints the directory, the users with their password, the rooms,
// the number of posts, the number of attachments, the number of comments, the
// number of reactions, the number of tasks, the number of notifications and the
// private messages to stdout. The tasks are
// written with the posts and comments and some are moved on the board after
// that. Their due dates are relative to the clock of the demo.
// The notifications are not written by any step of its own: they come from the
// mentions, replies, assignments and room additions of the data above, made
// through the services, so that the rules of SPEC §7.9 and D30 apply. The
// private messages (SPEC §7.11) come last: see makeDemoMessages. Later steps
// add their own demo data here. With -size large it then adds the data set of
// SPEC §10 (see makeLargeDemo) and prints its counts.
func runDemo(ctx context.Context, args []string, env func(string) string, stdout, stderr io.Writer, hasher auth.Hasher) error {
	var dataDir, size, databaseURL string
	flags := flag.NewFlagSet("demo", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&dataDir, "data", envString(env, "CLOWDER_DATA", config.DefaultDataDir),
		"data directory, made if missing and empty (CLOWDER_DATA)")
	databaseFromEnv := urlFlag(flags, &databaseURL, "database-url", env, "CLOWDER_DATABASE_URL", "",
		"PostgreSQL URL, postgres://...; empty: SQLite in the data directory (CLOWDER_DATABASE_URL)")
	flags.StringVar(&size, "size", demoSizeSmall,
		"size of the demo: small, or large, the data set of SPEC §10 (200 users, 100 rooms, 100,000 posts and comments, 20,000 tasks)")
	if err := flags.Parse(args); err != nil {
		return errors.NewErr(tagFlagsInvalid, err)
	}
	if flags.NArg() > 0 {
		return errors.New(tagFlagsInvalid, "unexpected arguments", "args", flags.Args())
	}
	databaseFromEnv()
	if dataDir == "" {
		return errors.New(tagFlagsInvalid, "data directory is empty", "flag", "data")
	}
	scale, err := demoScaleOf(size)
	if err != nil {
		return err
	}
	cfg := config.Default()
	cfg.DatabaseURL = databaseURL
	if err := cfg.Validate(); err != nil {
		return err
	}

	if err := os.MkdirAll(dataDir, dataDirMode); err != nil {
		return errors.NewErr(tagDataDirCreateFailed, err).SetMetadata("dir", dataDir)
	}
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		return errors.NewErr(tagDataDirReadFailed, err).SetMetadata("dir", dataDir)
	}
	if len(entries) > 0 {
		return errors.New(tagDataDirNotEmpty, "the demo needs an empty data directory",
			"dir", dataDir, "entries", len(entries))
	}
	_, _ = fmt.Fprintf(stdout, "demo data directory is ready: %s\n", dataDir)
	return fillDemo(ctx, dataDir, databaseURL, scale, stdout, stderr, hasher)
}

// fillDemo makes the demo in the empty data directory dataDir, with its
// database in dataDir or, when databaseURL is set, in PostgreSQL: the small demo
// and, when scale is not nil, the large one on top of it (makeLargeDemo).
// runDemo calls it with the scale of the flag -size; the tests call it with a
// smaller one.
func fillDemo(ctx context.Context, dataDir, databaseURL string, scale *demoScale, stdout, stderr io.Writer, hasher auth.Hasher) error {
	logger := newLogger(config.DefaultLogLevel, stderr)
	st, err := openStore(ctx, dataDir, databaseURL, clock.Real(), logger)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := st.Close(); closeErr != nil {
			logger.Error("closing the store failed", "err", closeErr)
		}
	}()
	avatarStore, err := files.New(filepath.Join(dataDir, filesDir, avatarsDir))
	if err != nil {
		return err
	}
	accounts := app.NewAccounts(st, clock.Real(), hasher, app.AccountsConfig{
		InvitationLifetime: config.DefaultInvitationLifetime, SessionLifetime: config.DefaultSessionLifetime,
		Avatars: avatarStore, Logger: logger,
	})
	_, _ = fmt.Fprintf(stdout, "demo users (password %s):\n", demoPassword)
	actors := map[string]app.Actor{}
	for _, d := range demoUsers {
		inv, err := accounts.CreateSystemInvitation(ctx, d.role)
		if err != nil {
			return err
		}
		u, err := accounts.SignUp(ctx, inv.Token, d.username, d.displayName, demoPassword)
		if err != nil {
			return err
		}
		actors[u.Username] = app.Actor{UserID: u.ID, Username: u.Username, Role: u.Role}
		_, _ = fmt.Fprintf(stdout, "  %-6s %-6s %s\n", u.Username, u.Role, u.DisplayName)
	}
	if err := makeDemoAvatars(ctx, accounts, actors, stdout); err != nil {
		return err
	}
	fileStore, err := files.New(filepath.Join(dataDir, filesDir))
	if err != nil {
		return err
	}
	made, err := makeDemoRooms(ctx, app.NewRooms(st, fileStore, logger, nil), actors, stdout)
	if err != nil {
		return err
	}
	clk := clock.Real()
	now := clk.Now()
	posts := app.NewPosts(st, clk, markdown.New(), fileStore, app.PostsConfig{
		MaxAttachmentSize: config.DefaultMaxAttachmentSize,
		MaxAttachments:    config.DefaultMaxAttachmentsPerPost,
		Logger:            logger,
	}, nil)
	written, err := makeDemoPosts(ctx, posts, made, actors, now, stdout)
	if err != nil {
		return err
	}
	comments, err := makeDemoComments(ctx, posts, written, actors, now, stdout)
	if err != nil {
		return err
	}
	if err := makeDemoReactions(ctx, posts, written, comments, actors, stdout); err != nil {
		return err
	}
	if err := makeDemoBoard(ctx, app.NewBoard(st, clk, nil), made, actors, stdout); err != nil {
		return err
	}
	if err := reportDemoNotifications(ctx, app.NewNotifications(st, nil), actors, stdout); err != nil {
		return err
	}
	messages := app.NewMessages(st, clk, fileStore, app.MessagesConfig{
		MaxAttachmentSize: config.DefaultMaxAttachmentSize,
		MaxAttachments:    config.DefaultMaxAttachmentsPerPost,
		Logger:            logger,
	}, nil)
	if err := makeDemoMessages(ctx, messages, actors, stdout); err != nil {
		return err
	}
	if err := makeDemoPreferences(ctx, app.NewPrefs(st, nil), made, actors, stdout); err != nil {
		return err
	}
	if scale == nil {
		return nil
	}
	return makeLargeDemo(ctx, *scale, largeDeps{
		st: st, rooms: app.NewRooms(st, fileStore, logger, nil), messages: messages,
		files: fileStore, hasher: hasher, clk: clk,
	}, stdout)
}

// makeDemoRooms makes the rooms of the demo through the rooms service, as the
// users who would make them: admin makes the visible rooms general and random,
// everyone joins general, alice and bob join random, and alice makes the
// private room secret and adds bob. actors holds the demo users by username.
// It returns the rooms by name.
func makeDemoRooms(ctx context.Context, rooms *app.Rooms, actors map[string]app.Actor, stdout io.Writer) (map[string]app.Room, error) {
	_, _ = fmt.Fprintln(stdout, "demo rooms:")
	made := map[string]app.Room{}
	for _, d := range []struct {
		owner       string
		name        string
		description string
		visibility  app.Visibility
	}{
		{"admin", "general", "Everything", app.VisibilityVisible},
		{"admin", "random", "Off topic", app.VisibilityVisible},
		{"alice", "secret", "Just for a few of us", app.VisibilityPrivate},
	} {
		room, err := rooms.Create(ctx, actors[d.owner], app.RoomInput{Name: d.name, Description: d.description, Visibility: d.visibility})
		if err != nil {
			return nil, err
		}
		made[d.name] = room
		_, _ = fmt.Fprintf(stdout, "  %-8s %-7s %s\n", room.Name, room.Visibility, room.Description)
	}
	for _, d := range demoUsers {
		if err := rooms.Join(ctx, actors[d.username], made["general"].ID); err != nil {
			return nil, err
		}
	}
	for _, username := range []string{"alice", "bob"} {
		if err := rooms.Join(ctx, actors[username], made["random"].ID); err != nil {
			return nil, err
		}
	}
	if err := rooms.AddMember(ctx, actors["alice"], made["secret"].ID, actors["bob"].UserID); err != nil {
		return nil, err
	}
	return made, nil
}

// demoPosts are the posts of the demo, in the order they are written: the
// oldest first, so that the newest shows at the top of a feed.
var demoPosts = []struct {
	room   string
	author string
	format app.Format
	body   string
	tags   string
	// attach names the files that the post carries: demoFile makes their content.
	attach []string
}{
	{"general", "alice", app.FormatMarkdown, "# Welcome to Clowder\n\nThis is **general**, where the whole team talks. A few things to know:\n\n- Posts can be *text* or *markdown*\n- Tags go in their own field\n- Everything is kept on your own server\n\nThe project lives at [the Clowder repository](https://github.com/rm4n0s/clowder). Say hi to @bob and @carol, who keep the board.", "welcome, help", nil},
	{"general", "bob", app.FormatText, "Quick question: where do we keep the design notes?\nI looked in the wiki and found nothing.", "help design", nil},
	{"general", "carol", app.FormatMarkdown, "## Release checklist\n\n1. Run the tests\n2. Tag the release\n3. Write the notes\n\nThe check that most people forget:\n\n```go\nfunc main() {\n\tfmt.Println(\"ship it\")\n}\n```\n\nThe same check in Python and in the shell:\n\n```python\ndef ship(checks: list[str]) -> bool:\n    # every check must pass\n    return all(run(c) == 0 for c in checks)\n```\n\n```sh\n#!/bin/sh\nset -e\ngo test -race ./... && echo \"ship it\"\n```", "release", nil},
	{"general", "alice", app.FormatMarkdown, "### Design review on Friday\n\n- [x] Mockups are ready\n- [ ] Colors are chosen\n\nSee the [style guide](https://example.com/style) and bring `questions`.", "design, release", []string{"mockup.png", "notes.txt"}},
	{"general", "bob", app.FormatMarkdown, "Thanks all! I put the design notes in a ~~wiki page~~ shared folder, with a table of the screens:\n\n| Screen | Owner |\n|---|---|\n| Feed | Alice |\n| Board | Carol |", "design", nil},
	{"random", "alice", app.FormatText, "Anyone up for lunch at noon? https://example.com/menu has the menu.", "", nil},
	{"random", "bob", app.FormatMarkdown, "Today's **mood**: coffee, then more coffee.", "coffee", nil},
	{"secret", "alice", app.FormatMarkdown, "Only a few of us read this room. Let's plan the *surprise* for the release party.", "release, party", nil},
}

// makeDemoPosts writes the posts of the demo through the posts service, as the
// users who would write them. rooms holds the rooms by name, actors the demo
// users by username. It returns the posts, in the order of demoPosts.
func makeDemoPosts(ctx context.Context, posts *app.Posts, rooms map[string]app.Room, actors map[string]app.Actor, now time.Time, stdout io.Writer) ([]app.Post, error) {
	written := make([]app.Post, 0, len(demoPosts))
	attachments := 0
	for i, d := range demoPosts {
		var uploads []app.Upload
		for _, name := range d.attach {
			content, err := demoFile(name)
			if err != nil {
				return nil, err
			}
			u, err := posts.Upload(ctx, actors[d.author], name, bytes.NewReader(content))
			if err != nil {
				return nil, err
			}
			uploads = append(uploads, u)
		}
		p, err := posts.Create(ctx, actors[d.author], rooms[d.room].ID, app.PostInput{
			Format:  d.format,
			Body:    d.body,
			Tags:    app.ParseTags(d.tags),
			Uploads: uploads,
			Tasks:   demoTaskInputs(demoPostTasks[i], actors, now),
		})
		if err != nil {
			return nil, err
		}
		attachments += len(uploads)
		written = append(written, p)
	}
	_, _ = fmt.Fprintf(stdout, "demo posts: %d\n", len(demoPosts))
	_, _ = fmt.Fprintf(stdout, "demo attachments: %d\n", attachments)
	return written, nil
}

// Size of the demo image in pixels.
const (
	demoImageWidth  = 320
	demoImageHeight = 200
)

// writeDemoImage writes the demo image to w: a 320 by 200 PNG whose red grows
// to the right and whose green grows downwards.
func writeDemoImage(w io.Writer) error {
	img := image.NewRGBA(image.Rect(0, 0, demoImageWidth, demoImageHeight))
	for y := range demoImageHeight {
		for x := range demoImageWidth {
			img.Set(x, y, color.RGBA{
				R: uint8(x * 255 / (demoImageWidth - 1)),
				G: uint8(y * 255 / (demoImageHeight - 1)),
				B: 160,
				A: 255,
			})
		}
	}
	if err := png.Encode(w, img); err != nil {
		return errors.NewErr(tagDemoImageFailed, err)
	}
	return nil
}

// demoFile returns the content of a file of the demo: mockup.png is a small
// gradient made here, notes.txt a few lines of text.
func demoFile(name string) ([]byte, error) {
	switch name {
	case "mockup.png":
		var b bytes.Buffer
		if err := writeDemoImage(&b); err != nil {
			return nil, err
		}
		return b.Bytes(), nil
	case "notes.txt":
		return []byte("Design review notes\n\n- Mockups are ready\n- Pick the colors on Friday\n- Ship the style guide\n"), nil
	default:
		return nil, errors.New(tagDemoFileUnknown, "the demo has no such file", "name", name)
	}
}

// demoComments are the comments of the demo, in the order they are written.
// post is the index in demoPosts of the post of the thread; parent is the id of
// the comment that this one answers, or empty for a reply to the post itself.
// A comment with deletedBy is deleted by that user once all are written, so
// that it stays as a deleted placeholder when it has replies (D21).
var demoComments = []struct {
	id        string
	post      int
	parent    string
	author    string
	format    app.Format
	body      string
	tags      string
	deletedBy string
}{
	// The thread under bob's question: three levels (alice, bob, carol), and a
	// deleted comment of carol's that has a reply.
	{"a1", 1, "", "alice", app.FormatText, "Try the shared folder first, then the wiki.", "", ""},
	{"a2", 1, "a1", "bob", app.FormatText, "Found it in the shared folder, thanks!", "", ""},
	{"a3", 1, "a2", "carol", app.FormatMarkdown, "Great. I added a link to the **design notes** in the wiki too, @alice.", "", ""},
	{"a4", 1, "", "carol", app.FormatText, "Someone moved them last week, I think.", "", "carol"},
	{"a5", 1, "a4", "alice", app.FormatText, "Do you remember where to?", "", ""},
	// The thread under the release checklist, with tags on its first comment.
	{"b1", 2, "", "bob", app.FormatMarkdown, "Add *update the changelog* to the list.", "release, changelog", ""},
	{"b2", 2, "b1", "carol", app.FormatText, "Done, thanks!", "", ""},
	{"b3", 2, "b2", "alice", app.FormatText, "Can we have a script for it?", "", ""},
	{"b4", 2, "b3", "bob", app.FormatMarkdown, "I can write one on `Friday`.", "", ""},
	// A third person in the thread, within the minute of the others: carol
	// follows her checklist, so her notification of the replies reads "Dave
	// and 2 others replied" (SPEC §7.15).
	{"b5", 2, "b4", "dave", app.FormatText, "Count me in for the Friday script.", "", ""},
}

// makeDemoComments writes the comments of the demo through the posts service,
// as the users who would write them, under the posts that makeDemoPosts wrote
// (written, in the order of demoPosts), and then the deletions. actors holds
// the demo users by username. It returns the comments by their id in
// demoComments.
func makeDemoComments(ctx context.Context, posts *app.Posts, written []app.Post, actors map[string]app.Actor, now time.Time, stdout io.Writer) (map[string]app.Post, error) {
	byID := map[string]app.Post{}
	for _, d := range demoComments {
		parent := written[d.post]
		if d.parent != "" {
			parent = byID[d.parent]
		}
		c, err := posts.Reply(ctx, actors[d.author], parent.ID, app.PostInput{
			Format: d.format,
			Body:   d.body,
			Tags:   app.ParseTags(d.tags),
			Tasks:  demoTaskInputs(demoCommentTasks[d.id], actors, now),
		})
		if err != nil {
			return nil, err
		}
		byID[d.id] = c
	}
	deleted := 0
	for _, d := range demoComments {
		if d.deletedBy == "" {
			continue
		}
		if err := posts.Delete(ctx, actors[d.deletedBy], byID[d.id].ID); err != nil {
			return nil, err
		}
		deleted++
	}
	_, _ = fmt.Fprintf(stdout, "demo comments: %d (%d deleted)\n", len(demoComments), deleted)
	return byID, nil
}

// demoReactions are the reactions of the demo, in the order they are made, so
// that the emoji of a post come in this order. A reaction is on the comment
// with the id in comment (as in demoComments) or, when that is empty, on the
// post with the index post in demoPosts. Every user is in general, where all of
// them are. The newest post has none with a party popper: the proof and the
// browser tests put that one on it live.
var demoReactions = []struct {
	post    int
	comment string
	emoji   string
	by      []string
}{
	{0, "", "\U0001F44D", []string{"bob", "carol", "admin"}},
	{0, "", "\u2764\uFE0F", []string{"bob"}},
	{1, "", "\U0001F914", []string{"alice"}},
	{2, "", "\U0001F680", []string{"alice", "bob"}},
	{2, "", "\U0001F440", []string{"admin"}},
	{3, "", "\u2705", []string{"bob"}},
	{3, "", "\U0001F44D", []string{"carol"}},
	{4, "", "\U0001F64F", []string{"alice", "carol"}},
	{0, "a2", "\U0001F44D", []string{"alice"}},
}

// makeDemoReactions puts the reactions of the demo on the posts and comments
// that the earlier steps wrote (written, in the order of demoPosts; comments by
// id), through the posts service, as the users who would react. actors holds
// the demo users by username.
func makeDemoReactions(ctx context.Context, posts *app.Posts, written []app.Post, comments map[string]app.Post, actors map[string]app.Actor, stdout io.Writer) error {
	n := 0
	for _, d := range demoReactions {
		target := written[d.post]
		if d.comment != "" {
			target = comments[d.comment]
		}
		for _, username := range d.by {
			if _, err := posts.ToggleReaction(ctx, actors[username], target.ID, d.emoji); err != nil {
				return err
			}
			n++
		}
	}
	_, _ = fmt.Fprintf(stdout, "demo reactions: %d\n", n)
	return nil
}

// demoTask is a task of the demo: assignees are usernames, due is the number
// of days from today (negative for the past), or nil for no due date.
type demoTask struct {
	title     string
	assignees []string
	due       *int
}

// dueIn returns the due date of a demoTask: days from today.
func dueIn(days int) *int { return &days }

// demoPostTasks are the tasks written with the posts of the demo, by the index
// of the post in demoPosts: seven in general, two in secret. They all start in
// the first column of the board (demoMoves moves some). One task is overdue:
// its due date is two days before the demo is made.
var demoPostTasks = map[int][]demoTask{
	0: {
		{"Write the welcome guide", []string{"alice"}, dueIn(3)},
		{"Invite the rest of the team", []string{"admin"}, nil},
	},
	2: {
		{"Run the tests", []string{"bob"}, dueIn(1)},
		{"Tag the release", []string{"carol"}, dueIn(5)},
		{"Write the release notes", []string{"alice", "carol"}, dueIn(-2)},
	},
	3: {
		{"Pick the colors", []string{"alice", "bob"}, dueIn(2)},
	},
	7: {
		{"Book the party room", []string{"alice", "bob"}, dueIn(10)},
		{"Order the cake", []string{"bob"}, nil},
	},
}

// demoCommentTasks are the tasks written with the comments of the demo, by the
// id of the comment in demoComments.
var demoCommentTasks = map[string][]demoTask{
	"b4": {{"Write the changelog script", []string{"bob"}, dueIn(4)}},
}

// demoMoves are the cards that makeDemoBoard moves after all are written:
// the room, the title, the index of the column to move it to (In progress is
// 1, Done 2) and the user who moves it.
var demoMoves = []struct {
	room   string
	title  string
	column int
	by     string
}{
	{"general", "Write the welcome guide", 2, "alice"},
	{"general", "Pick the colors", 1, "alice"},
	{"general", "Run the tests", 1, "bob"},
	{"secret", "Order the cake", 1, "bob"},
}

// demoTaskInputs makes the task rows of the editor from demo tasks. actors
// holds the demo users by username; the due dates count from now.
func demoTaskInputs(tasks []demoTask, actors map[string]app.Actor, now time.Time) []app.TaskInput {
	var out []app.TaskInput
	for _, t := range tasks {
		in := app.TaskInput{Title: t.title}
		for _, name := range t.assignees {
			in.Assignees = append(in.Assignees, actors[name].UserID)
		}
		if t.due != nil {
			due := now.AddDate(0, 0, *t.due)
			in.DueDate = &due
		}
		out = append(out, in)
	}
	return out
}

// makeDemoBoard moves the cards of demoMoves through the board service, as
// the users who would drag them, and prints the number of tasks of the demo
// (they are written with the posts and comments). rooms holds the rooms by
// name, actors the demo users by username.
func makeDemoBoard(ctx context.Context, board *app.Board, rooms map[string]app.Room, actors map[string]app.Actor, stdout io.Writer) error {
	for _, m := range demoMoves {
		view, err := board.Board(ctx, actors[m.by], rooms[m.room].ID)
		if err != nil {
			return err
		}
		var taskID uint64
		for _, c := range view.Columns {
			for _, card := range c.Tasks {
				if card.Title == m.title {
					taskID = card.ID
				}
			}
		}
		if taskID == 0 {
			return errors.New(tagDemoTaskUnknown, "the demo has no such task", "room", m.room, "title", m.title)
		}
		if err := board.MoveTask(ctx, actors[m.by], taskID, view.Columns[m.column].ID, 0); err != nil {
			return err
		}
	}
	n := 0
	for _, tasks := range demoPostTasks {
		n += len(tasks)
	}
	for _, tasks := range demoCommentTasks {
		n += len(tasks)
	}
	_, _ = fmt.Fprintf(stdout, "demo tasks: %d\n", n)
	return nil
}

// reportDemoNotifications prints the number of notifications the demo users
// have, in total and per user (the notifications themselves were made by the
// services while the posts, comments, tasks and rooms were written). actors
// holds the demo users by username.
func reportDemoNotifications(ctx context.Context, notes *app.Notifications, actors map[string]app.Actor, stdout io.Writer) error {
	total := 0
	var perUser []string
	for _, d := range demoUsers {
		page, err := notes.List(ctx, actors[d.username], 0, 50)
		if err != nil {
			return err
		}
		total += len(page.Notifications)
		perUser = append(perUser, fmt.Sprintf("%s %d", d.username, len(page.Notifications)))
	}
	_, _ = fmt.Fprintf(stdout, "demo notifications: %d (%s)\n", total, strings.Join(perUser, ", "))
	return nil
}

// demoMessage is a message of the demo. conv names the conversation (the
// direct one of alice and bob, or the group), reply is the number (from 1,
// in demoMessages) of the message it answers, or 0.
type demoMessage struct {
	conv  string
	by    string
	body  string
	reply int
}

// The conversations of the private messages of the demo.
const (
	demoDirect = "alice-bob"
	demoGroup  = "Launch crew"
)

// demoMessages are the private messages of the demo, in the order they are
// sent. The group is read to its newest message by everyone but bob wrote it;
// bob has not read the last message of the direct conversation.
var demoMessages = []demoMessage{
	{demoDirect, "alice", "Hi Bob, did you find the design notes?", 0},
	{demoDirect, "bob", "Yes, they are in the shared folder. Thanks for asking!", 1},
	{demoDirect, "alice", "Great! Lunch at noon?", 0},
	{demoGroup, "alice", "Welcome to the launch crew! This is where we plan the release.", 0},
	{demoGroup, "carol", "The release checklist is in general, have a look.", 0},
	{demoGroup, "bob", "Thanks Carol, I will tag the release on Friday.", 5},
}

// makeDemoMessages makes the private conversations of the demo through the
// messages service, as the users who would write in them: the direct
// conversation of alice and bob, and the group "Launch crew" of alice, bob and
// carol, with the messages of demoMessages (some are replies), an edit, two
// reactions, and read state so that bob has one unread conversation. It prints
// the number of messages, conversations, reactions and edits and the unread
// conversations of bob. actors holds the demo users by username.
func makeDemoMessages(ctx context.Context, messages *app.Messages, actors map[string]app.Actor, stdout io.Writer) error {
	direct, err := messages.StartDirect(ctx, actors["alice"], actors["bob"].UserID)
	if err != nil {
		return err
	}
	group, err := messages.StartGroup(ctx, actors["alice"], []uint64{actors["bob"].UserID, actors["carol"].UserID}, demoGroup)
	if err != nil {
		return err
	}
	conversations := map[string]uint64{demoDirect: direct, demoGroup: group}
	sent := make([]app.Message, 0, len(demoMessages))
	for _, d := range demoMessages {
		in := app.MessageInput{Body: d.body}
		if d.reply != 0 {
			in.ReplyToID = sent[d.reply-1].ID
		}
		m, err := messages.Send(ctx, actors[d.by], conversations[d.conv], in)
		if err != nil {
			return err
		}
		sent = append(sent, m)
	}
	// Alice corrects her lunch invitation.
	if _, err := messages.Edit(ctx, actors["alice"], sent[2].ID, "Great! Lunch at noon? My treat."); err != nil {
		return err
	}
	reactions := 0
	for _, r := range []struct {
		by    string
		msg   int
		emoji string
	}{
		{"alice", 2, "\u2764\uFE0F"},
		{"bob", 3, "\U0001F44D"},
	} {
		if _, err := messages.React(ctx, actors[r.by], sent[r.msg-1].ID, r.emoji); err != nil {
			return err
		}
		reactions++
	}
	// Everyone has read the group; bob has not read the last message of the
	// direct conversation.
	for _, username := range []string{"alice", "carol"} {
		if err := messages.MarkRead(ctx, actors[username], group, sent[len(sent)-1].ID); err != nil {
			return err
		}
	}
	unread, err := messages.UnreadCount(ctx, actors["bob"])
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stdout, "demo messages: %d in %d conversations (%d reactions, 1 edited; bob has %d unread)\n",
		len(sent), len(conversations), reactions, unread)
	return nil
}

// makeDemoPreferences gives alice notification preferences of her own in the
// private room "secret" (SPEC §7.15): only mentions notify her there, and
// without a sound. rooms holds the demo rooms by name and actors the demo users
// by username.
func makeDemoPreferences(ctx context.Context, prefs *app.Prefs, rooms map[string]app.Room, actors map[string]app.Actor, stdout io.Writer) error {
	notify, sound := app.PlaceNotifyMentions, app.PlaceSoundOff
	if _, err := prefs.UpdatePlace(ctx, actors["alice"], app.RoomRef(rooms["secret"].ID), app.PlaceUpdate{Notify: &notify, Sound: &sound}); err != nil {
		return err
	}
	_, _ = fmt.Fprintln(stdout, "demo notification preferences: alice hears only mentions of #secret, without a sound")
	return nil
}

const (
	tagDemoTaskUnknown   = "DemoTaskUnknown"
	tagDataDirNotEmpty   = "DataDirNotEmpty"
	tagDataDirReadFailed = "DataDirReadFailed"
	tagDemoImageFailed   = "DemoImageFailed"
	tagDemoFileUnknown   = "DemoFileUnknown"
)
