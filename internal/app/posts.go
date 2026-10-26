package app

import (
	"context"
	"html/template"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/rm4n0s/clowder/internal/clock"
	"github.com/rm4n0s/clowder/internal/markdown"
	"github.com/rm4n0s/clowder/internal/store"
	"github.com/rm4n0s/errors"
)

// Format says how the body of a post is shown. It is the store's type, so the
// web layer can name the values without importing the store.
type Format = store.Format

// The two formats of SPEC §7.3.
const (
	// FormatText is a body shown as written: escaped, line breaks kept, links
	// made clickable.
	FormatText Format = store.FormatText
	// FormatMarkdown is a body rendered as markdown.
	FormatMarkdown Format = store.FormatMarkdown
)

// The rules of SPEC §7.3 and §7.7 for what a person types in the editor.
const (
	postBodyMaxLength = 20_000
	postMaxTags       = 10
	tagMinLength      = 1
	tagMaxLength      = 50
	// tagSuggestLimit is the number of names that SuggestTags returns at most.
	tagSuggestLimit = 10
	// feedDefaultLimit is the page size of the feed when the caller gives none.
	feedDefaultLimit = 30
	// feedMaxLimit is the largest page the feed gives.
	feedMaxLimit = 50
	// tagShownLength is how much of a wrong tag a message quotes.
	tagShownLength = 20
)

// The names of the fields of a post in the metadata "fields" of a
// TagInvalidInput error.
const (
	// FieldBody names the body of a post.
	FieldBody = "body"
	// FieldFormat names the format of a post.
	FieldFormat = "format"
	// FieldTags names the tags of a post.
	FieldTags = "tags"
)

// PostInput is what a person types in the editor when they write or edit a
// post.
type PostInput struct {
	// Format is how the body is shown: FormatText or FormatMarkdown.
	Format Format
	// Body is the text of the post: at most 20,000 characters, not empty.
	// Whitespace at its end is dropped.
	Body string
	// Tags are the labels of the post, as typed (ParseTags splits the field of
	// the editor). Each is normalized (SPEC §7.7); at most 10 remain.
	Tags []string
}

// UserRef names a user on a post.
type UserRef struct {
	// ID is the ID of the user.
	ID uint64
	// Username is the username of the user.
	Username string
	// DisplayName is the name shown to other people.
	DisplayName string
}

// Post is a post as the services show it to one actor.
type Post struct {
	// ID is the ID of the post. Newer posts have larger IDs.
	ID uint64
	// RoomID is the ID of the room of the post.
	RoomID uint64
	// RoomName is the name of the room of the post.
	RoomName string
	// Author is the author; the zero value for a deleted post.
	Author UserRef
	// Format says how the body is shown.
	Format Format
	// Body is the text as written, for the edit form; empty for a deleted post.
	Body string
	// HTML is the body rendered safely (SPEC D9); empty for a deleted post.
	HTML template.HTML
	// Tags are the normalized tags, in the order the author gave them; empty
	// for a deleted post.
	Tags []string
	// CommentCount is the number of comments under the post, at any depth.
	CommentCount int
	// CreatedAt is when the post was written.
	CreatedAt time.Time
	// EditedAt is when the author last edited the post; nil if never.
	EditedAt *time.Time
	// Deleted is true for a post that stays only because it has replies (D21).
	Deleted bool
	// CanEdit is true when the asking actor may edit the post: its author, if
	// the post is not deleted.
	CanEdit bool
	// CanDelete is true when the asking actor may delete the post: its author
	// or an admin, if the post is not deleted.
	CanDelete bool
}

// FeedPage is a page of the feed of a room.
type FeedPage struct {
	// Posts are the posts of the page, newest first.
	Posts []Post
	// HasNext is true when older posts exist.
	HasNext bool
	// NextBefore is the beforeID that asks for the next page: the ID of the
	// last post of this page. It is 0 when HasNext is false.
	NextBefore uint64
}

// Posts is the service of posts, their tags and their feeds (SPEC §7.3, §7.7).
// Make it with NewPosts. It is safe for concurrent use.
type Posts struct {
	st  *store.Store
	clk clock.Clock
	md  *markdown.Renderer
}

// NewPosts makes the service. The clock times the edits and deletions; the
// renderer makes the HTML of the bodies.
func NewPosts(st *store.Store, clk clock.Clock, md *markdown.Renderer) *Posts {
	return &Posts{st: st, clk: clk, md: md}
}

// ParseTags splits the tags field of the editor on spaces and commas. It
// returns the pieces as typed, without empty ones; PostInput.Tags takes them
// and the service normalizes them.
func ParseTags(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || unicode.IsSpace(r)
	})
}

// Create writes a post in a room and its audit event post.created (the event
// never holds the body). Only members of the room post: a room that the actor
// may not read fails with TagNotFound, a visible room that the actor has not
// joined with TagForbidden. It fails with TagInvalidInput, with all the
// problems in the metadata "fields", for input that breaks a rule.
func (p *Posts) Create(ctx context.Context, actor Actor, roomID uint64, in PostInput) (Post, error) {
	var created store.Post
	var room store.Room
	err := p.st.Tx(ctx, func(tx *store.Store) error {
		var err error
		room, err = loadReadableRoom(ctx, tx, actor, roomID)
		if err != nil {
			return err
		}
		member, err := isMember(ctx, tx, actor.UserID, room.ID)
		if err != nil {
			return err
		}
		if !member {
			return errors.New(TagForbidden, "only members post",
				"userID", actor.UserID, "roomID", room.ID)
		}
		clean, fields := checkPostInput(in)
		if len(fields) > 0 {
			return errors.New(TagInvalidInput, "the post has mistakes", "fields", fields)
		}
		who, err := displayNameOf(ctx, tx, actor.UserID)
		if err != nil {
			return err
		}
		created = store.Post{RoomID: room.ID, AuthorID: actor.UserID, Format: clean.Format, Body: clean.Body}
		if err := tx.CreatePost(ctx, &created); err != nil {
			return err
		}
		if err := tx.ReplacePostTags(ctx, created.ID, clean.Tags); err != nil {
			return err
		}
		// TODO(step 11.1): mentions and notifications.
		// TODO(step 7.1): publish live events.
		return recordAudit(ctx, tx, auditEntry{
			ActorID:    actor.UserID,
			Action:     ActionPostCreated,
			RoomID:     room.ID,
			EntityType: EntityPost,
			EntityID:   created.ID,
			Summary:    who + " posted in #" + room.Name,
			Details: map[string]any{
				"format": string(clean.Format),
				"length": utf8.RuneCountInString(clean.Body),
				"tags":   clean.Tags,
			},
		})
	})
	if err != nil {
		return Post{}, err
	}
	return p.viewOne(ctx, actor, room, created.ID)
}

// Edit changes the body, the format and the tags of a post and writes
// post.edited with only what changed (for the body that it changed and its
// length, never the text). It does nothing, and writes no event, when nothing
// changed. Only the author may: another reader gets TagForbidden. A room that
// the actor may not read, a missing post and a deleted post fail with
// TagNotFound. It fails with TagInvalidInput, with all the problems in the
// metadata "fields", for input that breaks a rule.
func (p *Posts) Edit(ctx context.Context, actor Actor, postID uint64, in PostInput) (Post, error) {
	var room store.Room
	err := p.st.Tx(ctx, func(tx *store.Store) error {
		row, err := tx.PostByID(ctx, postID)
		if err != nil {
			return err
		}
		room, err = loadReadableRoom(ctx, tx, actor, row.RoomID)
		if err != nil {
			return err
		}
		if row.DeletedAt != nil {
			return errors.New(TagNotFound, "the post is deleted", "reason", "deleted", "postID", postID)
		}
		if row.AuthorID != actor.UserID {
			return errors.New(TagForbidden, "only the author edits a post",
				"userID", actor.UserID, "postID", postID)
		}
		clean, fields := checkPostInput(in)
		if len(fields) > 0 {
			return errors.New(TagInvalidInput, "the post has mistakes", "fields", fields)
		}
		oldTags, err := tx.TagNamesOfPosts(ctx, []uint64{row.ID})
		if err != nil {
			return err
		}
		before := oldTags[row.ID]

		details := map[string]any{}
		bodyChanged := clean.Body != row.Body
		formatChanged := clean.Format != row.Format
		tagsChanged := !sameTags(before, clean.Tags)
		if bodyChanged {
			details["body"] = bodyChangeDetails(clean.Body)
		}
		if formatChanged {
			details["format"] = changeOf(string(row.Format), string(clean.Format))
		}
		if tagsChanged {
			details["tags"] = changeOf(nonNil(before), clean.Tags)
		}
		if len(details) == 0 {
			return nil
		}
		who, err := displayNameOf(ctx, tx, actor.UserID)
		if err != nil {
			return err
		}
		if err := tx.UpdatePostContent(ctx, row.ID, clean.Format, clean.Body, p.clk.Now()); err != nil {
			return err
		}
		if tagsChanged {
			if err := tx.ReplacePostTags(ctx, row.ID, clean.Tags); err != nil {
				return err
			}
		}
		// TODO(step 11.1): rebuild the mentions and notify the new ones.
		// TODO(step 7.1): publish live events.
		return recordAudit(ctx, tx, auditEntry{
			ActorID:    actor.UserID,
			Action:     ActionPostEdited,
			RoomID:     room.ID,
			EntityType: EntityPost,
			EntityID:   row.ID,
			Summary:    who + " edited a post in #" + room.Name,
			Details:    details,
		})
	})
	if err != nil {
		return Post{}, err
	}
	return p.viewOne(ctx, actor, room, postID)
}

// Delete deletes a post and writes post.deleted with {"kept": bool}. The
// author or an admin who can read the room may; another reader gets
// TagForbidden, and a room that the actor may not read, a missing post and a
// deleted post fail with TagNotFound. A post with replies stays as a deleted
// placeholder with no body, tags, reactions, mentions or attachments; a post
// without replies disappears with everything on it (D21).
func (p *Posts) Delete(ctx context.Context, actor Actor, postID uint64) error {
	return p.st.Tx(ctx, func(tx *store.Store) error {
		row, err := tx.PostByID(ctx, postID)
		if err != nil {
			return err
		}
		room, err := loadReadableRoom(ctx, tx, actor, row.RoomID)
		if err != nil {
			return err
		}
		if row.DeletedAt != nil {
			return errors.New(TagNotFound, "the post is deleted", "reason", "deleted", "postID", postID)
		}
		if row.AuthorID != actor.UserID && !actor.IsAdmin() {
			return errors.New(TagForbidden, "only the author and admins delete a post",
				"userID", actor.UserID, "postID", postID)
		}
		who, err := displayNameOf(ctx, tx, actor.UserID)
		if err != nil {
			return err
		}
		kept, err := tx.PostHasReplies(ctx, row.ID)
		if err != nil {
			return err
		}
		if kept {
			// TODO(step 9.1): the tasks of the post leave the board.
			if err := tx.DeletePostExtras(ctx, row.ID); err != nil {
				return err
			}
			if err := tx.MarkPostDeleted(ctx, row.ID, p.clk.Now()); err != nil {
				return err
			}
		} else if err := tx.DeletePost(ctx, row.ID); err != nil {
			return err
		}
		// TODO(step 6.1): remove the files of the attachments.
		// TODO(step 7.1): publish live events.
		return recordAudit(ctx, tx, auditEntry{
			ActorID:    actor.UserID,
			Action:     ActionPostDeleted,
			RoomID:     room.ID,
			EntityType: EntityPost,
			EntityID:   row.ID,
			Summary:    who + " deleted a post in #" + room.Name,
			Details:    map[string]any{"kept": kept},
		})
	})
}

// Feed returns a page of the top-level posts of a room that the actor may
// read, newest first. beforeID 0 starts from the newest post; another value
// returns the posts older than it. limit is the page size: 30 when it is 0 or
// less, at most 50. A deleted post that has replies appears as deleted. For a
// room that the actor may not read it fails with TagNotFound.
func (p *Posts) Feed(ctx context.Context, actor Actor, roomID, beforeID uint64, limit int) (FeedPage, error) {
	if limit <= 0 {
		limit = feedDefaultLimit
	}
	limit = min(limit, feedMaxLimit)
	room, err := loadReadableRoom(ctx, p.st, actor, roomID)
	if err != nil {
		return FeedPage{}, err
	}
	// One more than a page tells whether an older page exists.
	rows, err := p.st.ListFeed(ctx, room.ID, beforeID, limit+1)
	if err != nil {
		return FeedPage{}, err
	}
	hasNext := len(rows) > limit
	if hasNext {
		rows = rows[:limit]
	}
	views, err := p.postViews(ctx, p.st, actor, room, rows)
	if err != nil {
		return FeedPage{}, err
	}
	page := FeedPage{Posts: views, HasNext: hasNext}
	if hasNext {
		page.NextBefore = rows[len(rows)-1].ID
	}
	return page, nil
}

// Get returns a post of a room that the actor may read; a deleted post that
// was kept comes back with Deleted set. A missing post and a room that the
// actor may not read fail with TagNotFound.
func (p *Posts) Get(ctx context.Context, actor Actor, postID uint64) (Post, error) {
	row, err := p.st.PostByID(ctx, postID)
	if err != nil {
		return Post{}, err
	}
	room, err := loadReadableRoom(ctx, p.st, actor, row.RoomID)
	if err != nil {
		return Post{}, err
	}
	views, err := p.postViews(ctx, p.st, actor, room, []store.Post{row})
	if err != nil {
		return Post{}, err
	}
	return views[0], nil
}

// SuggestTags returns up to 10 tag names that start with prefix (normalized
// like a tag), used on posts in rooms that the actor can read, ordered by
// name. An empty prefix suggests the first names.
func (p *Posts) SuggestTags(ctx context.Context, actor Actor, prefix string) ([]string, error) {
	prefix = strings.ToLower(strings.TrimLeft(strings.TrimSpace(prefix), "#"))
	return p.st.SuggestTagNames(ctx, actor.UserID, prefix, tagSuggestLimit)
}

// viewOne reads the post postID, which is in room, and makes its view.
func (p *Posts) viewOne(ctx context.Context, actor Actor, room store.Room, postID uint64) (Post, error) {
	row, err := p.st.PostByID(ctx, postID)
	if err != nil {
		return Post{}, err
	}
	views, err := p.postViews(ctx, p.st, actor, room, []store.Post{row})
	if err != nil {
		return Post{}, err
	}
	return views[0], nil
}

// postViews makes the views of posts of one room for the actor, in the order
// of rows, with two queries: the tags and the comment counts. The rows have
// Author loaded.
func (p *Posts) postViews(ctx context.Context, st *store.Store, actor Actor, room store.Room, rows []store.Post) ([]Post, error) {
	ids := make([]uint64, len(rows))
	for i, row := range rows {
		ids[i] = row.ID
	}
	tags, err := st.TagNamesOfPosts(ctx, ids)
	if err != nil {
		return nil, err
	}
	counts, err := st.CommentCounts(ctx, ids)
	if err != nil {
		return nil, err
	}
	views := make([]Post, len(rows))
	for i, row := range rows {
		v := Post{
			ID:           row.ID,
			RoomID:       room.ID,
			RoomName:     room.Name,
			Format:       row.Format,
			Tags:         []string{},
			CommentCount: counts[row.ID],
			CreatedAt:    row.CreatedAt,
			EditedAt:     row.EditedAt,
			Deleted:      row.DeletedAt != nil,
		}
		if !v.Deleted {
			v.Body = row.Body
			v.Tags = nonNil(tags[row.ID])
			if row.Author != nil {
				v.Author = UserRef{ID: row.Author.ID, Username: row.Author.Username, DisplayName: row.Author.DisplayName}
			}
			v.HTML, err = p.md.Render(markdown.Format(row.Format), row.Body)
			if err != nil {
				return nil, err
			}
			v.CanEdit = row.AuthorID == actor.UserID
			v.CanDelete = row.AuthorID == actor.UserID || actor.IsAdmin()
		}
		views[i] = v
	}
	return views, nil
}

// checkPostInput applies the rules of SPEC §7.3 and §7.7 to in. It returns the
// input as it is stored (body without trailing whitespace, tags normalized and
// without duplicates) and the problems as field name to message; the problems
// are empty when all rules hold.
func checkPostInput(in PostInput) (PostInput, map[string]string) {
	fields := map[string]string{}
	clean := PostInput{Format: in.Format}
	if in.Format != FormatText && in.Format != FormatMarkdown {
		fields[FieldFormat] = "Choose text or markdown."
	}

	clean.Body = strings.TrimRightFunc(in.Body, unicode.IsSpace)
	// TODO(step 6.1/9.1): allow an empty body with attachments or tasks.
	switch {
	case !utf8.ValidString(clean.Body):
		fields[FieldBody] = "Use valid text."
	case clean.Body == "":
		fields[FieldBody] = "Write something."
	case utf8.RuneCountInString(clean.Body) > postBodyMaxLength:
		fields[FieldBody] = "Use at most 20,000 characters."
	}

	tags, problem := normalizeTags(in.Tags)
	if problem != "" {
		fields[FieldTags] = problem
	}
	clean.Tags = tags
	return clean, fields
}

// normalizeTags normalizes the tags as typed (SPEC §7.7): trimmed, without
// leading "#", lower-cased. A tag is 1 to 50 characters of letters, digits,
// "-" and "_". Blank entries and duplicates are dropped; at most 10 remain.
// The second result is the message for the first problem, or empty.
func normalizeTags(raw []string) ([]string, string) {
	tags := []string{}
	for _, r := range raw {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		name := strings.ToLower(strings.TrimLeft(r, "#"))
		if !validTag(name) {
			shown := []rune(r)
			quoted := string(shown)
			if len(shown) > tagShownLength {
				quoted = string(shown[:tagShownLength]) + "…"
			}
			return tags, "\"" + quoted + "\" is not a tag: use 1 to 50 letters, digits, - or _."
		}
		if !slices.Contains(tags, name) {
			tags = append(tags, name)
		}
	}
	if len(tags) > postMaxTags {
		return tags, "At most 10 tags."
	}
	return tags, ""
}

// validTag reports whether name, already lower-cased, is a tag.
func validTag(name string) bool {
	n := utf8.RuneCountInString(name)
	if n < tagMinLength || n > tagMaxLength || !utf8.ValidString(name) {
		return false
	}
	for _, r := range name {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-' && r != '_' {
			return false
		}
	}
	return true
}

// sameTags reports whether a and b hold the same tags, in any order.
func sameTags(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	x, y := slices.Clone(a), slices.Clone(b)
	slices.Sort(x)
	slices.Sort(y)
	return slices.Equal(x, y)
}

// nonNil returns s, or an empty slice for nil, so that it is stored as [] and
// not null.
func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
