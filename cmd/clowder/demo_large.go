package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/auth"
	"github.com/rm4n0s/clowder/internal/clock"
	"github.com/rm4n0s/clowder/internal/files"
	"github.com/rm4n0s/clowder/internal/store"
	"github.com/rm4n0s/errors"
)

// The names of the sizes of the demo (the flag -size).
const (
	demoSizeSmall = "small"
	demoSizeLarge = "large"
)

// demoScale is the size of the large demo: what makeLargeDemo adds to the
// small demo, as totals of the database it ends with. Only tests make a
// scale of their own (smaller than largeScale); users reach the large one
// with "-size large".
type demoScale struct {
	// users is the number of users, the four of the small demo included.
	users int
	// rooms is the number of rooms, the three of the small demo included;
	// every privateEvery-th room that is added is private.
	rooms        int
	privateEvery int
	// posts is the number of posts and comments together, the small demo's
	// included; commentShare of them are comments.
	posts        int
	commentShare float64
	// tasks is the number of tasks, the small demo's included.
	tasks int
	// directChats and groupChats are the private conversations that are
	// added, each with chatMessages messages.
	directChats, groupChats, chatMessages int
}

// largeScale is the data set of SPEC §10.
var largeScale = demoScale{
	users:        200,
	rooms:        100,
	privateEvery: 7,
	posts:        100_000,
	commentShare: 0.30,
	tasks:        20_000,
	directChats:  8,
	groupChats:   3,
	chatMessages: 40,
}

// demoScaleOf returns the scale of the size named on the command line: nil
// for the small demo, which has no scale. Any other name fails with
// DemoSizeInvalid.
func demoScaleOf(size string) (*demoScale, error) {
	switch size {
	case demoSizeSmall:
		return nil, nil
	case demoSizeLarge:
		sc := largeScale
		return &sc, nil
	default:
		return nil, errors.New(tagDemoSizeInvalid, "the size of the demo is small or large",
			"flag", "size", "size", size)
	}
}

// The shares and limits of the large demo's generator.
const (
	// largeSeedA and largeSeedB seed the random source: the same scale always
	// makes the same data.
	largeSeedA, largeSeedB = 20261027, 1
	// largeSpan is the time the posts are spread over, ending a minute before
	// the demo is made.
	largeSpan = 365 * 24 * time.Hour
	// largeMarkdownShare is the share of posts written in markdown.
	largeMarkdownShare = 0.35
	// largeMentionShare is the share of posts that mention someone.
	largeMentionShare = 0.06
	// largeReactionShare is the share of posts with reactions.
	largeReactionShare = 0.12
	// largeAttachmentShare is the share of posts with an attachment.
	largeAttachmentShare = 0.004
	// largeDueShare is the share of tasks with a due date; largeAssignedShare
	// the share with assignees.
	largeDueShare      = 0.40
	largeAssignedShare = 0.60
	// largeRoomSkew makes the busiest room much busier than the quietest.
	largeRoomSkew = 0.8
	// largeTagSkew makes the first tags the most used.
	largeTagSkew = 0.9
	// largeWordSkew makes the first words of the vocabulary the most common.
	largeWordSkew = 2.0
)

// largeDeps are the services and stores that the large demo works through.
type largeDeps struct {
	st       *store.Store
	rooms    *app.Rooms
	messages *app.Messages
	files    *files.Store
	hasher   auth.Hasher
	clk      clock.Clock
}

// largeRoom is a room as the generator sees it.
type largeRoom struct {
	id      uint64
	name    string
	members []uint64
	// columns are the IDs of the board columns, left to right.
	columns []uint64
}

// largeItem is a post or comment the generator made: what a later post, task
// or reaction needs to know about it.
type largeItem struct {
	id     uint64
	room   int
	author uint64
	// created is when it was written.
	created time.Time
}

// makeLargeDemo adds the data set of SPEC §10 to the database that the small
// demo made, as sc says, and prints its counts. Users, posts, comments, tags,
// reactions, mentions, attachments, tasks and their audit events are written
// in one transaction through BulkCreate, and the full-text index is rebuilt
// from them, so that the result is what the services would have made one call
// at a time: every post and task has its audit event post.created or
// task.created, every task is in a column of its room with positions 0, 1, 2,
// ..., and every row of posts has a row in the index. Rooms, memberships and
// the private conversations go through the services. No notifications are
// made for the bulk data. It is deterministic.
func makeLargeDemo(ctx context.Context, sc demoScale, d largeDeps, stdout io.Writer) error {
	started := time.Now()
	rng := rand.New(rand.NewPCG(largeSeedA, largeSeedB))
	now := d.clk.Now()
	g := &largeGen{ctx: ctx, sc: sc, d: d, rng: rng, now: now, stdout: stdout}
	if err := g.makeUsers(); err != nil {
		return err
	}
	if err := g.makeRooms(); err != nil {
		return err
	}
	if err := g.makeContent(); err != nil {
		return err
	}
	if err := g.makeChats(); err != nil {
		return err
	}
	return g.report(time.Since(started))
}

// largeGen holds the state of makeLargeDemo.
type largeGen struct {
	ctx    context.Context
	sc     demoScale
	d      largeDeps
	rng    *rand.Rand
	now    time.Time
	stdout io.Writer

	// users are all the users, the ones of the small demo first; fresh are
	// those this demo added.
	users, fresh []store.User
	// admin acts for the visible rooms.
	admin app.Actor
	// rooms are all the rooms; weights is the cumulative weight of each, by
	// which the posts choose theirs.
	rooms   []largeRoom
	weights []float64
}

// actorOf makes the actor of a user.
func actorOf(u store.User) app.Actor {
	return app.Actor{UserID: u.ID, Username: u.Username, Role: u.Role}
}

// makeUsers adds the members who are not in the small demo, with one password
// hash for all of them (they all have the demo password, and hashing 200
// passwords with the real parameters would take half a minute).
func (g *largeGen) makeUsers() error {
	existing, err := g.d.st.ListUsers(g.ctx)
	if err != nil {
		return err
	}
	hash, err := g.d.hasher.Hash(demoPassword)
	if err != nil {
		return err
	}
	var rows []store.User
	for i := len(existing); i < g.sc.users; i++ {
		first := largeFirstNames[i%len(largeFirstNames)]
		last := largeLastNames[(i/len(largeFirstNames)+i)%len(largeLastNames)]
		rows = append(rows, store.User{
			Username:     first + strconv.Itoa(i),
			DisplayName:  strings.ToUpper(first[:1]) + first[1:] + " " + last,
			PasswordHash: hash,
			Role:         store.RoleMember,
		})
	}
	err = g.d.st.Tx(g.ctx, func(tx *store.Store) error {
		return store.BulkCreate(g.ctx, tx, rows)
	})
	if err != nil {
		return err
	}
	g.users, err = g.d.st.ListUsers(g.ctx)
	if err != nil {
		return err
	}
	g.fresh = g.users[len(existing):]
	for _, u := range g.users {
		if u.Username == "admin" {
			g.admin = actorOf(u)
		}
	}
	return nil
}

// makeRooms adds rooms through the rooms service until there are sc.rooms:
// every privateEvery-th is private, made by a member who adds the others;
// the visible ones are made by the admin and joined by members. Everybody
// joins "general", half of the users join "random". It then reads the rooms
// with their members and columns.
func (g *largeGen) makeRooms() error {
	rooms := g.d.rooms
	existing, err := g.d.st.ListRooms(g.ctx)
	if err != nil {
		return err
	}
	byName := map[string]store.Room{}
	for _, r := range existing {
		byName[r.Name] = r
	}
	// The members (not the admin) make the private rooms and fill the rooms.
	var members []store.User
	for _, u := range g.users {
		if u.Role == store.RoleMember {
			members = append(members, u)
		}
	}
	for _, u := range g.fresh {
		if err := rooms.Join(g.ctx, actorOf(u), byName["general"].ID); err != nil {
			return err
		}
		if g.rng.Float64() < 0.5 {
			if err := rooms.Join(g.ctx, actorOf(u), byName["random"].ID); err != nil {
				return err
			}
		}
	}
	maxVisible := max(6, len(members)*7/10)
	for i := 0; len(existing)+i < g.sc.rooms; i++ {
		topic := largeTopics[i%len(largeTopics)]
		if round := i / len(largeTopics); round > 0 {
			topic += "-" + strconv.Itoa(round+1)
		}
		private := g.sc.privateEvery > 0 && i%g.sc.privateEvery == g.sc.privateEvery-1
		in := app.RoomInput{Name: topic, Description: "Everything about " + topic, Visibility: app.VisibilityVisible}
		owner := g.admin
		size := 5 + int(math.Pow(g.rng.Float64(), 2)*float64(maxVisible-5))
		if private {
			in.Visibility = app.VisibilityPrivate
			owner = actorOf(members[g.rng.IntN(len(members))])
			size = 3 + g.rng.IntN(18)
		}
		room, err := rooms.Create(g.ctx, owner, in)
		if err != nil {
			return err
		}
		for _, k := range g.rng.Perm(len(members)) {
			if size <= 1 {
				break
			}
			u := members[k]
			if u.ID == owner.UserID {
				continue
			}
			if private {
				err = rooms.AddMember(g.ctx, owner, room.ID, u.ID)
			} else {
				err = rooms.Join(g.ctx, actorOf(u), room.ID)
			}
			if err != nil {
				return err
			}
			size--
		}
	}
	return g.readRooms()
}

// readRooms loads the rooms with their members and columns, and the weights
// by which posts choose a room: a few rooms are far busier than the rest.
func (g *largeGen) readRooms() error {
	rows, err := g.d.st.ListRooms(g.ctx)
	if err != nil {
		return err
	}
	// rankOf is the busyness rank of each room: 0 is the busiest.
	rankOf := make([]int, len(rows))
	for rank, k := range g.rng.Perm(len(rows)) {
		rankOf[k] = rank
	}
	g.rooms = make([]largeRoom, len(rows))
	g.weights = make([]float64, len(rows))
	total := 0.0
	for k, r := range rows {
		members, err := g.d.st.ListRoomMembers(g.ctx, r.ID)
		if err != nil {
			return err
		}
		columns, err := g.d.st.ListBoardColumns(g.ctx, r.ID)
		if err != nil {
			return err
		}
		room := largeRoom{id: r.ID, name: r.Name}
		for _, m := range members {
			room.members = append(room.members, m.UserID)
		}
		for _, c := range columns {
			room.columns = append(room.columns, c.ID)
		}
		g.rooms[k] = room
		total += 1 / math.Pow(float64(rankOf[k]+1), largeRoomSkew)
		g.weights[k] = total
	}
	return nil
}

// pickRoom chooses a room by weight.
func (g *largeGen) pickRoom() int {
	x := g.rng.Float64() * g.weights[len(g.weights)-1]
	return min(sort.SearchFloat64s(g.weights, x), len(g.weights)-1)
}

// largeRows are the rows of the bulk transaction.
type largeRows struct {
	posts       []store.Post
	postTags    []store.PostTag
	attachments []store.Attachment
	reactions   []store.Reaction
	mentions    []store.Mention
	tasks       []store.Task
	assignees   []store.TaskAssignee
	audits      []store.AuditEvent
}

// makeContent writes the posts, comments, tags, reactions, mentions,
// attachments, tasks and audit events in one transaction and rebuilds the
// full-text index.
func (g *largeGen) makeContent() error {
	st := g.d.st
	stats, err := st.Stats(g.ctx)
	if err != nil {
		return err
	}
	newPosts := g.sc.posts - int(stats.Posts+stats.Comments)
	newTasks := g.sc.tasks - int(stats.Tasks)
	if newPosts <= 0 || newTasks < 0 {
		return errors.New(tagDemoScaleInvalid, "the scale is smaller than the small demo",
			"posts", g.sc.posts, "tasks", g.sc.tasks)
	}
	gen, err := g.newContentGen(newPosts, newTasks)
	if err != nil {
		return err
	}
	rows, err := gen.generate()
	if err != nil {
		return err
	}
	return st.Tx(g.ctx, func(tx *store.Store) error {
		if err := store.BulkCreate(g.ctx, tx, gen.newTags); err != nil {
			return err
		}
		if err := store.BulkCreate(g.ctx, tx, rows.posts); err != nil {
			return err
		}
		if err := store.BulkCreate(g.ctx, tx, rows.postTags); err != nil {
			return err
		}
		if err := store.BulkCreate(g.ctx, tx, rows.attachments); err != nil {
			return err
		}
		if err := store.BulkCreate(g.ctx, tx, rows.reactions); err != nil {
			return err
		}
		if err := store.BulkCreate(g.ctx, tx, rows.mentions); err != nil {
			return err
		}
		if err := store.BulkCreate(g.ctx, tx, rows.tasks); err != nil {
			return err
		}
		if err := store.BulkCreate(g.ctx, tx, rows.assignees); err != nil {
			return err
		}
		if err := store.BulkCreate(g.ctx, tx, rows.audits); err != nil {
			return err
		}
		return tx.RebuildSearchIndex(g.ctx)
	})
}

// contentGen generates the rows of makeContent.
type contentGen struct {
	*largeGen
	newPosts int
	// taskSlots[i] is the number of tasks that the i-th new post carries.
	taskSlots []int
	// names and displayNames are by user ID.
	names, displayNames map[uint64]string
	// tagIDs are the tags by name; tagCum are the cumulative weights of
	// largeTags; newTags are the tags the demo adds.
	tagIDs  map[string]uint64
	tagCum  []float64
	newTags []store.Tag
	// nextPost, nextTask and nextTag are the next primary keys.
	nextPost, nextTask uint64
	// columnNext is the next position of each column, by column ID.
	columnNext map[uint64]int
	// png is the content of the attachments that are images.
	png []byte
	// firstTime and step place the i-th post in time.
	firstTime time.Time
	step      time.Duration
}

// newContentGen reads what the generator needs from the database: the
// existing tags, the next primary keys and the next position of each column.
func (g *largeGen) newContentGen(newPosts, newTasks int) (*contentGen, error) {
	c := &contentGen{largeGen: g, newPosts: newPosts, columnNext: map[uint64]int{}}
	c.names = map[uint64]string{}
	c.displayNames = map[uint64]string{}
	for _, u := range g.users {
		c.names[u.ID] = u.Username
		c.displayNames[u.ID] = u.DisplayName
	}
	tags, err := g.d.st.ListTags(g.ctx)
	if err != nil {
		return nil, err
	}
	c.tagIDs = map[string]uint64{}
	var nextTag uint64 = 1
	for _, t := range tags {
		c.tagIDs[t.Name] = t.ID
		nextTag = max(nextTag, t.ID+1)
	}
	total := 0.0
	for i, name := range largeTags {
		if _, ok := c.tagIDs[name]; !ok {
			c.tagIDs[name] = nextTag
			c.newTags = append(c.newTags, store.Tag{ID: nextTag, Name: name, CreatedAt: g.now})
			nextTag++
		}
		total += 1 / math.Pow(float64(i+1), largeTagSkew)
		c.tagCum = append(c.tagCum, total)
	}
	if c.nextPost, err = store.NextID[store.Post](g.ctx, g.d.st); err != nil {
		return nil, err
	}
	if c.nextTask, err = store.NextID[store.Task](g.ctx, g.d.st); err != nil {
		return nil, err
	}
	for _, room := range g.rooms {
		for _, col := range room.columns {
			existing, err := g.d.st.ListTasksOfColumn(g.ctx, col)
			if err != nil {
				return nil, err
			}
			c.columnNext[col] = len(existing)
		}
	}
	c.taskSlots = make([]int, newPosts)
	for range newTasks {
		c.taskSlots[g.rng.IntN(newPosts)]++
	}
	var img bytes.Buffer
	if err := writeDemoImage(&img); err != nil {
		return nil, err
	}
	c.png = img.Bytes()
	c.step = largeSpan / time.Duration(newPosts)
	c.firstTime = g.now.Add(-time.Minute - c.step*time.Duration(newPosts))
	return c, nil
}

// generate makes the rows of all new posts, in the order they are written.
func (c *contentGen) generate() (largeRows, error) {
	var rows largeRows
	rows.posts = make([]store.Post, 0, c.newPosts)
	// roomTop are the top-level posts of each room (indexes in items);
	// threads are the comments of each top-level post, by its ID.
	roomTop := make([][]int, len(c.rooms))
	threads := map[uint64][]uint64{}
	var items []largeItem
	for i := range c.newPosts {
		ri := c.pickRoom()
		room := c.rooms[ri]
		created := c.firstTime.Add(c.step * time.Duration(i))
		author := room.members[c.rng.IntN(len(room.members))]
		p := store.Post{ID: c.nextPost, CreatedAt: created, RoomID: room.id, AuthorID: author}
		c.nextPost++
		if len(roomTop[ri]) > 0 && c.rng.Float64() < c.sc.commentShare {
			// A comment under a recent post of the room: under the post itself
			// or under one of its comments, so that threads get deeper.
			n := len(roomTop[ri])
			root := items[roomTop[ri][max(0, n-1-int(float64(n)*math.Pow(c.rng.Float64(), 2)))]]
			parent := root.id
			if comments := threads[root.id]; len(comments) > 0 && c.rng.Float64() < 0.6 {
				parent = comments[c.rng.IntN(len(comments))]
			}
			p.ParentID, p.RootID = &parent, &root.id
			threads[root.id] = append(threads[root.id], p.ID)
		} else {
			roomTop[ri] = append(roomTop[ri], len(items))
		}
		items = append(items, largeItem{id: p.ID, room: ri, author: author, created: created})
		if err := c.fillPost(&p, room, &rows); err != nil {
			return largeRows{}, err
		}
		rows.posts = append(rows.posts, p)
		if err := c.addTasks(items[len(items)-1], c.taskSlots[i], &rows); err != nil {
			return largeRows{}, err
		}
	}
	return rows, nil
}

// fillPost gives p its format and body, and writes the rows that belong to
// it: tags, mentions, reactions, an attachment, and the audit event.
func (c *contentGen) fillPost(p *store.Post, room largeRoom, rows *largeRows) error {
	p.Format = store.FormatText
	markdown := c.rng.Float64() < largeMarkdownShare
	if markdown {
		p.Format = store.FormatMarkdown
	}
	var mentioned []uint64
	var cc []string
	if c.rng.Float64() < largeMentionShare {
		for range 1 + c.rng.IntN(2) {
			id := room.members[c.rng.IntN(len(room.members))]
			if id != p.AuthorID && !contains(mentioned, id) {
				mentioned = append(mentioned, id)
				cc = append(cc, "@"+c.names[id])
			}
		}
	}
	p.Body = c.body(markdown, p.ParentID == nil, cc)
	for _, id := range mentioned {
		rows.mentions = append(rows.mentions, store.Mention{PostID: p.ID, UserID: id, CreatedAt: p.CreatedAt})
	}
	tags := c.pickTags()
	for _, name := range tags {
		rows.postTags = append(rows.postTags, store.PostTag{PostID: p.ID, TagID: c.tagIDs[name], CreatedAt: p.CreatedAt})
	}
	if c.rng.Float64() < largeReactionShare {
		var seen []string
		for range 1 + c.rng.IntN(4) {
			user := room.members[c.rng.IntN(len(room.members))]
			emoji := largeEmoji[c.rng.IntN(len(largeEmoji))]
			key := strconv.FormatUint(user, 10) + emoji
			if !contains(seen, key) {
				seen = append(seen, key)
				rows.reactions = append(rows.reactions, store.Reaction{PostID: p.ID, UserID: user, Emoji: emoji, CreatedAt: p.CreatedAt})
			}
		}
	}
	attachments := 0
	if c.rng.Float64() < largeAttachmentShare {
		a, err := c.attachment(p)
		if err != nil {
			return err
		}
		rows.attachments = append(rows.attachments, a)
		attachments = 1
	}
	details := map[string]any{
		"format":      string(p.Format),
		"length":      utf8.RuneCountInString(p.Body),
		"tags":        tags,
		"attachments": attachments,
	}
	entity, verb := store.EntityPost, " posted in #"
	if p.ParentID != nil {
		entity, verb = store.EntityComment, " commented in #"
		details["rootID"], details["parentID"] = *p.RootID, *p.ParentID
	}
	if n := c.taskSlots[len(rows.posts)]; n > 0 {
		details["tasks"] = n
	}
	raw, err := marshalDetails(details)
	if err != nil {
		return err
	}
	rows.audits = append(rows.audits, store.AuditEvent{
		CreatedAt: p.CreatedAt, ActorID: p.AuthorID, Action: store.ActionPostCreated, RoomID: room.id,
		EntityType: entity, EntityID: p.ID, Summary: c.displayNames[p.AuthorID] + verb + room.name, Details: raw,
	})
	return nil
}

// addTasks writes n tasks of the post it: each in a column of its room, at the
// end of the column, with its audit event.
func (c *contentGen) addTasks(it largeItem, n int, rows *largeRows) error {
	room := c.rooms[it.room]
	for range n {
		colIdx := 0
		switch x := c.rng.Float64(); {
		case x < 0.55:
		case x < 0.80 || len(room.columns) < 3:
			colIdx = min(1, len(room.columns)-1)
		default:
			colIdx = 2
		}
		col := room.columns[colIdx]
		t := store.Task{
			ID: c.nextTask, CreatedAt: it.created, RoomID: room.id, PostID: it.id, ColumnID: col,
			Position: c.columnNext[col],
			Title:    largeVerbs[c.rng.IntN(len(largeVerbs))] + " " + largeNouns[c.rng.IntN(len(largeNouns))],
		}
		c.nextTask++
		c.columnNext[col]++
		due := "none"
		if c.rng.Float64() < largeDueShare {
			day := c.now.AddDate(0, 0, c.rng.IntN(60)-20)
			d := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC)
			t.DueDate = &d
			due = d.Format("2006-01-02")
		}
		var names []string
		if c.rng.Float64() < largeAssignedShare {
			for range 1 + c.rng.IntN(2) {
				id := room.members[c.rng.IntN(len(room.members))]
				if !contains(names, c.names[id]) {
					names = append(names, c.names[id])
					rows.assignees = append(rows.assignees, store.TaskAssignee{TaskID: t.ID, UserID: id, CreatedAt: it.created})
				}
			}
		}
		raw, err := marshalDetails(map[string]any{
			"postID":    it.id,
			"title":     map[string]any{"old": nil, "new": t.Title},
			"column":    map[string]any{"old": nil, "new": columnName(colIdx)},
			"assignees": map[string]any{"old": nil, "new": names},
			"dueDate":   map[string]any{"old": nil, "new": due},
		})
		if err != nil {
			return err
		}
		rows.tasks = append(rows.tasks, t)
		rows.audits = append(rows.audits, store.AuditEvent{
			CreatedAt: it.created, ActorID: it.author, Action: store.ActionTaskCreated, RoomID: room.id,
			EntityType: store.EntityTask, EntityID: t.ID,
			Summary: c.displayNames[it.author] + " added the task \"" + t.Title + "\" in #" + room.name, Details: raw,
		})
	}
	return nil
}

// columnNames are the names of the columns that every room starts with.
var columnNames = []string{"To do", "In progress", "Done"}

// columnName returns the name of the i-th default column.
func columnName(i int) string {
	return columnNames[min(i, len(columnNames)-1)]
}

// marshalDetails makes the JSON of the details of an audit event.
func marshalDetails(details map[string]any) (string, error) {
	raw, err := json.Marshal(details)
	if err != nil {
		return "", errors.NewErr(tagDemoDetailsFailed, err)
	}
	return string(raw), nil
}

// attachment saves a small file of the demo for the post p and returns the
// row that describes it: a text file or the demo image.
func (c *contentGen) attachment(p *store.Post) (store.Attachment, error) {
	name := "notes-" + strconv.FormatUint(p.ID, 10) + ".txt"
	content := []byte(c.sentence(10, 60) + "\n")
	if c.rng.Float64() < 0.3 {
		name = "screenshot-" + strconv.FormatUint(p.ID, 10) + ".png"
		content = c.png
	}
	saved, err := c.d.files.Save(c.ctx, bytes.NewReader(content), int64(len(content))+1)
	if err != nil {
		return store.Attachment{}, err
	}
	a := store.Attachment{
		CreatedAt: p.CreatedAt, PostID: p.ID, UploaderID: p.AuthorID, FileName: name,
		ContentType: saved.ContentType, Size: saved.Size, StorageKey: saved.Key,
	}
	if saved.Width > 0 && saved.Height > 0 {
		w, h := saved.Width, saved.Height
		a.Width, a.Height = &w, &h
	}
	return a, nil
}

// pickTags returns the tags of a post: none for most, up to three.
func (c *contentGen) pickTags() []string {
	n := 0
	switch x := c.rng.Float64(); {
	case x < 0.55:
	case x < 0.80:
		n = 1
	case x < 0.95:
		n = 2
	default:
		n = 3
	}
	tags := []string{}
	for range n {
		name := largeTags[min(sort.SearchFloat64s(c.tagCum, c.rng.Float64()*c.tagCum[len(c.tagCum)-1]), len(largeTags)-1)]
		if !contains(tags, name) {
			tags = append(tags, name)
		}
	}
	return tags
}

// word returns a word of the vocabulary; the first ones are the most common.
func (g *largeGen) word() string {
	return largeWords[int(math.Pow(g.rng.Float64(), largeWordSkew)*float64(len(largeWords)))]
}

// sentence returns between lo and hi words.
func (g *largeGen) sentence(lo, hi int) string {
	n := lo + g.rng.IntN(hi-lo+1)
	words := make([]string, n)
	for i := range words {
		words[i] = g.word()
	}
	s := strings.Join(words, " ")
	return strings.ToUpper(s[:1]) + s[1:] + "."
}

// body returns the text of a post or comment: a few sentences; in markdown
// with a heading (posts), emphasis, a list or code now and then. cc are the
// mentions to add at the end.
func (g *largeGen) body(markdown, post bool, cc []string) string {
	var sb strings.Builder
	if markdown && post && g.rng.Float64() < 0.5 {
		sb.WriteString("## " + g.sentence(2, 5) + "\n\n")
	}
	paragraphs := 1
	if post {
		paragraphs += g.rng.IntN(3)
	}
	for i := range paragraphs {
		if i > 0 {
			sb.WriteString("\n\n")
		}
		s := g.sentence(4, 25)
		if markdown {
			switch g.rng.IntN(4) {
			case 0:
				s = "**" + g.word() + "** " + s
			case 1:
				s += " See `" + g.word() + "`."
			}
		}
		sb.WriteString(s)
	}
	if markdown && g.rng.Float64() < 0.25 {
		sb.WriteString("\n\n- " + g.sentence(2, 6) + "\n- " + g.sentence(2, 6))
	}
	if len(cc) > 0 {
		sb.WriteString("\n\ncc " + strings.Join(cc, " "))
	}
	return sb.String()
}

// contains reports whether list has v.
func contains[T comparable](list []T, v T) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// makeChats adds private conversations through the messages service: direct
// ones between random users and groups of three to six, each with
// chatMessages messages, some of them replies.
func (g *largeGen) makeChats() error {
	sc := g.sc
	pick := func(n int) []store.User {
		var out []store.User
		for _, k := range g.rng.Perm(len(g.users))[:n] {
			out = append(out, g.users[k])
		}
		return out
	}
	var chats [][]store.User
	var ids []uint64
	for range sc.directChats {
		pair := pick(2)
		id, err := g.d.messages.StartDirect(g.ctx, actorOf(pair[0]), pair[1].ID)
		if err != nil {
			return err
		}
		chats, ids = append(chats, pair), append(ids, id)
	}
	for i := range sc.groupChats {
		crew := pick(3 + g.rng.IntN(4))
		var others []uint64
		for _, u := range crew[1:] {
			others = append(others, u.ID)
		}
		id, err := g.d.messages.StartGroup(g.ctx, actorOf(crew[0]), others, "Crew "+strconv.Itoa(i+1))
		if err != nil {
			return err
		}
		chats, ids = append(chats, crew), append(ids, id)
	}
	for i, id := range ids {
		var sent []app.Message
		for range sc.chatMessages {
			from := chats[i][g.rng.IntN(len(chats[i]))]
			in := app.MessageInput{Body: largeChatLines[g.rng.IntN(len(largeChatLines))]}
			if len(sent) > 0 && g.rng.Float64() < 0.2 {
				in.ReplyToID = sent[g.rng.IntN(len(sent))].ID
			}
			m, err := g.d.messages.Send(g.ctx, actorOf(from), id, in)
			if err != nil {
				return err
			}
			sent = append(sent, m)
		}
	}
	return nil
}

// report prints the counts of the database and the time it took. It fails
// with DemoInconsistent, listing the checks that found rows, when the data
// breaks a rule of the services.
func (g *largeGen) report(took time.Duration) error {
	s, err := g.d.st.Stats(g.ctx)
	if err != nil {
		return err
	}
	out := g.stdout
	_, _ = fmt.Fprintf(out, "large demo users: %d\n", s.Users)
	_, _ = fmt.Fprintf(out, "large demo rooms: %d (%d private), %d memberships, %d board columns\n", s.Rooms, s.PrivateRooms, s.Members, s.Columns)
	_, _ = fmt.Fprintf(out, "large demo posts and comments: %d (%d posts, %d comments)\n", s.Posts+s.Comments, s.Posts, s.Comments)
	_, _ = fmt.Fprintf(out, "large demo tags: %d (%d on posts), reactions: %d, mentions: %d, attachments: %d\n", s.Tags, s.PostTags, s.Reactions, s.Mentions, s.Attachments)
	_, _ = fmt.Fprintf(out, "large demo tasks: %d (%d assignments)\n", s.Tasks, s.Assignees)
	_, _ = fmt.Fprintf(out, "large demo conversations: %d with %d messages\n", s.Conversations, s.Messages)
	_, _ = fmt.Fprintf(out, "large demo audit events: %d, search index rows: %d\n", s.AuditEvents, s.Indexed)
	_, _ = fmt.Fprintf(out, "large demo made in %s\n", took.Round(time.Millisecond))
	bad := map[string]int64{
		"unindexed posts": s.Unindexed, "stray index rows": s.StrayIndexRows, "bad comments": s.BadComments,
		"authors outside their room": s.NonMemberAuthors, "bad board columns": s.BadColumns,
		"tasks in a foreign column": s.TasksInForeignColumn, "bad task positions": s.BadTaskPositions,
	}
	var problems []string
	for name, n := range bad {
		if n != 0 {
			problems = append(problems, name+" "+strconv.FormatInt(n, 10))
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return errors.New(tagDemoInconsistent, "the large demo broke a rule of the services", "problems", problems)
	}
	return nil
}

const (
	tagDemoSizeInvalid   = "DemoSizeInvalid"
	tagDemoScaleInvalid  = "DemoScaleInvalid"
	tagDemoDetailsFailed = "DemoDetailsFailed"
	tagDemoInconsistent  = "DemoInconsistent"
)
