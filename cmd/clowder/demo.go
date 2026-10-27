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
}

// runDemo prepares the data directory of the demo. The directory is made if it
// is missing and must otherwise be empty, so that demo never touches the data
// of a real instance. It then makes the database and the demo users in it,
// through the accounts service (an invitation and a sign-up for each), then
// the rooms through the rooms service, their posts and the comments under
// some of them through the posts service, and prints the directory, the users with their password, the rooms,
// the number of posts, the number of attachments and the number of comments
// to stdout.
// Later steps add their own demo data here.
func runDemo(ctx context.Context, args []string, env func(string) string, stdout, stderr io.Writer, hasher auth.Hasher) error {
	var dataDir string
	flags := flag.NewFlagSet("demo", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&dataDir, "data", envString(env, "CLOWDER_DATA", config.DefaultDataDir),
		"data directory, made if missing and empty (CLOWDER_DATA)")
	if err := flags.Parse(args); err != nil {
		return errors.NewErr(tagFlagsInvalid, err)
	}
	if flags.NArg() > 0 {
		return errors.New(tagFlagsInvalid, "unexpected arguments", "args", flags.Args())
	}
	if dataDir == "" {
		return errors.New(tagFlagsInvalid, "data directory is empty", "flag", "data")
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

	logger := newLogger(config.DefaultLogLevel, stderr)
	st, err := store.Open(ctx, filepath.Join(dataDir, databaseFile), clock.Real(), logger)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := st.Close(); closeErr != nil {
			logger.Error("closing the store failed", "err", closeErr)
		}
	}()
	accounts := app.NewAccounts(st, clock.Real(), hasher, app.AccountsConfig{InvitationLifetime: config.DefaultInvitationLifetime, SessionLifetime: config.DefaultSessionLifetime})
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
	fileStore, err := files.New(filepath.Join(dataDir, filesDir))
	if err != nil {
		return err
	}
	made, err := makeDemoRooms(ctx, app.NewRooms(st, fileStore, logger), actors, stdout)
	if err != nil {
		return err
	}
	posts := app.NewPosts(st, clock.Real(), markdown.New(), fileStore, app.PostsConfig{
		MaxAttachmentSize: config.DefaultMaxAttachmentSize,
		MaxAttachments:    config.DefaultMaxAttachmentsPerPost,
		Logger:            logger,
	})
	written, err := makeDemoPosts(ctx, posts, made, actors, stdout)
	if err != nil {
		return err
	}
	return makeDemoComments(ctx, posts, written, actors, stdout)
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
	{"general", "alice", app.FormatMarkdown, "# Welcome to Clowder\n\nThis is **general**, where the whole team talks. A few things to know:\n\n- Posts can be *text* or *markdown*\n- Tags go in their own field\n- Everything is kept on your own server\n\nThe project lives at [the Clowder repository](https://github.com/rm4n0s/clowder).", "welcome, help", nil},
	{"general", "bob", app.FormatText, "Quick question: where do we keep the design notes?\nI looked in the wiki and found nothing.", "help design", nil},
	{"general", "carol", app.FormatMarkdown, "## Release checklist\n\n1. Run the tests\n2. Tag the release\n3. Write the notes\n\nThe check that most people forget:\n\n```go\nfunc main() {\n\tfmt.Println(\"ship it\")\n}\n```", "release", nil},
	{"general", "alice", app.FormatMarkdown, "### Design review on Friday\n\n- [x] Mockups are ready\n- [ ] Colors are chosen\n\nSee the [style guide](https://example.com/style) and bring `questions`.", "design, release", []string{"mockup.png", "notes.txt"}},
	{"general", "bob", app.FormatMarkdown, "Thanks all! I put the design notes in a ~~wiki page~~ shared folder, with a table of the screens:\n\n| Screen | Owner |\n|---|---|\n| Feed | Alice |\n| Board | Carol |", "design", nil},
	{"random", "alice", app.FormatText, "Anyone up for lunch at noon? https://example.com/menu has the menu.", "", nil},
	{"random", "bob", app.FormatMarkdown, "Today's **mood**: coffee, then more coffee.", "coffee", nil},
	{"secret", "alice", app.FormatMarkdown, "Only a few of us read this room. Let's plan the *surprise* for the release party.", "release, party", nil},
}

// makeDemoPosts writes the posts of the demo through the posts service, as the
// users who would write them. rooms holds the rooms by name, actors the demo
// users by username. It returns the posts, in the order of demoPosts.
func makeDemoPosts(ctx context.Context, posts *app.Posts, rooms map[string]app.Room, actors map[string]app.Actor, stdout io.Writer) ([]app.Post, error) {
	written := make([]app.Post, 0, len(demoPosts))
	attachments := 0
	for _, d := range demoPosts {
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
	{"a3", 1, "a2", "carol", app.FormatMarkdown, "Great. I added a link to the **design notes** in the wiki too.", "", ""},
	{"a4", 1, "", "carol", app.FormatText, "Someone moved them last week, I think.", "", "carol"},
	{"a5", 1, "a4", "alice", app.FormatText, "Do you remember where to?", "", ""},
	// The thread under the release checklist, with tags on its first comment.
	{"b1", 2, "", "bob", app.FormatMarkdown, "Add *update the changelog* to the list.", "release, changelog", ""},
	{"b2", 2, "b1", "carol", app.FormatText, "Done, thanks!", "", ""},
	{"b3", 2, "b2", "alice", app.FormatText, "Can we have a script for it?", "", ""},
	{"b4", 2, "b3", "bob", app.FormatMarkdown, "I can write one on `Friday`.", "", ""},
}

// makeDemoComments writes the comments of the demo through the posts service,
// as the users who would write them, under the posts that makeDemoPosts wrote
// (written, in the order of demoPosts), and then the deletions. actors holds
// the demo users by username.
func makeDemoComments(ctx context.Context, posts *app.Posts, written []app.Post, actors map[string]app.Actor, stdout io.Writer) error {
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
		})
		if err != nil {
			return err
		}
		byID[d.id] = c
	}
	deleted := 0
	for _, d := range demoComments {
		if d.deletedBy == "" {
			continue
		}
		if err := posts.Delete(ctx, actors[d.deletedBy], byID[d.id].ID); err != nil {
			return err
		}
		deleted++
	}
	_, _ = fmt.Fprintf(stdout, "demo comments: %d (%d deleted)\n", len(demoComments), deleted)
	return nil
}

const (
	tagDataDirNotEmpty   = "DataDirNotEmpty"
	tagDataDirReadFailed = "DataDirReadFailed"
	tagDemoImageFailed   = "DemoImageFailed"
	tagDemoFileUnknown   = "DemoFileUnknown"
)
