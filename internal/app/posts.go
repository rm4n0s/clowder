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
	"github.com/rm4n0s/clowder/internal/files"
	"github.com/rm4n0s/clowder/internal/live"
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
	feedMaxLimit = 100
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
	// Uploads are the new files of the post, as Posts.Upload returned them.
	// They are attached to the post. The body may be empty when the post has
	// at least one attachment or one task.
	Uploads []Upload
	// KeepAttachments is used by Edit only: the IDs of the attachments of the
	// post that stay. Every other attachment of the post is removed, so a nil
	// list removes them all. An ID that is not an attachment of the post makes
	// the input invalid.
	KeepAttachments []uint64
	// Tasks are the tasks of the post (D17), at most 20. Create and Reply add
	// them to the end of the first column of the board. Edit matches them by
	// ID: the tasks of the post that are not listed are deleted, the listed
	// ones change, those with ID 0 are added; so a nil list deletes them all.
	// A task whose ID is not a task of the post makes the input invalid.
	Tasks []TaskInput
}

// UserRef names a user on a post.
type UserRef struct {
	// ID is the ID of the user.
	ID uint64
	// Username is the username of the user.
	Username string
	// DisplayName is the name shown to other people.
	DisplayName string
	// AvatarKey is the key of the user's avatar, for AvatarPath; empty when
	// the user has none.
	AvatarKey string
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
	// Attachments are the files of the post, in the order they were attached;
	// empty for a deleted post.
	Attachments []AttachmentView
	// Tasks are the tasks of the post, oldest first, with the column they are
	// in; empty for a deleted post.
	Tasks []TaskCard
	// CommentCount is the number of comments in the thread of the post, at any
	// depth, not counting deleted ones. It is 0 for a comment.
	CommentCount int
	// Reactions are the emoji that people put on the post, in the order the
	// emoji were first used, with their counts; empty for a deleted post.
	Reactions []ReactionCount
	// ParentID is the ID of the post or comment that this comment answers; 0
	// for a top-level post.
	ParentID uint64
	// RootID is the ID of the top-level post of the thread of this comment; 0
	// for a top-level post. For a reply to a top-level post it equals
	// ParentID.
	RootID uint64
	// IsComment is true for a comment and false for a top-level post.
	IsComment bool
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
	// CanReact is true when the asking actor may react to the post: a member
	// of its room, if the post is not deleted.
	CanReact bool
	// Following is true when the asking actor follows the post, so that its
	// comments notify them (SPEC §7.15). Only a top-level post can be
	// followed: for a comment it says whether the actor follows the
	// top-level post of its thread.
	Following bool
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

// Comment is a comment in the thread of a post, with its place in the tree.
type Comment struct {
	Post
	// Depth is how deep the comment is: 1 for a reply to the top-level post,
	// 2 for a reply to that, and so on.
	Depth int
	// ParentID is the ID of the post or comment that this comment answers. It
	// is the same as the embedded Post.ParentID.
	ParentID uint64
	// ParentAuthor is the author of the parent; the zero value when the parent
	// is a deleted comment or post.
	ParentAuthor UserRef
}

// Thread is a top-level post with all its comments, as one reader sees it.
type Thread struct {
	// Root is the top-level post of the thread.
	Root Post
	// Comments are the comments depth first: every comment comes before its
	// replies, and siblings are oldest first. A deleted comment that was kept
	// for its replies is included, as deleted.
	Comments []Comment
	// Focus is the ID of the comment that Thread was asked for, or 0 when it
	// was asked for the top-level post.
	Focus uint64
}

// Posts is the service of posts, their tags and their feeds (SPEC §7.3, §7.7).
// Make it with NewPosts. It is safe for concurrent use.
type Posts struct {
	st      *store.Store
	clk     clock.Clock
	md      *markdown.Renderer
	files   *files.Store
	cfg     PostsConfig
	cleaner fileCleaner
	pub     Publisher
	// presence tells who is online, for @here.
	presence live.Presence
}

// NewPosts makes the service. The clock times the edits and deletions; the
// renderer makes the HTML of the bodies; fs holds the files of the
// attachments; cfg has the limits of the attachments. pub receives the live
// events of the changes, after they are committed; nil publishes nothing.
func NewPosts(st *store.Store, clk clock.Clock, md *markdown.Renderer, fs *files.Store, cfg PostsConfig, pub Publisher) *Posts {
	cfg = cfg.withDefaults()
	return &Posts{st: st, clk: clk, md: md, files: fs, cfg: cfg, cleaner: newFileCleaner(fs, cfg.Logger), pub: orNop(pub),
		presence: orMemoryPresence(cfg.Presence)}
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
// problems in the metadata "fields", for input that breaks a rule. in.Uploads
// become the attachments of the post; when Create fails for any reason, their
// files are removed from disk. It saves the post's mentions (the users named
// as @username who are enabled and can read the room, D26) and, in the same
// transaction, notifies them and the users assigned to its tasks, never the
// actor; a user gets at most one notification (SPEC D30).
func (p *Posts) Create(ctx context.Context, actor Actor, roomID uint64, in PostInput) (Post, error) {
	var created store.Post
	var room store.Room
	var events []live.Event
	err := p.st.Tx(ctx, func(tx *store.Store) error {
		events = nil
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
		clean, fields := checkPostInput(in, len(in.Uploads))
		tasks, names, err := checkTasks(ctx, tx, room.ID, in.Tasks, nil, fields)
		if err != nil {
			return err
		}
		if err := p.checkUploads(ctx, tx, fields, in.Uploads, 0); err != nil {
			return err
		}
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
		if err := tx.CreateAttachments(ctx, attachmentRows(created.ID, actor.UserID, in.Uploads)); err != nil {
			return err
		}
		if err := tx.ReindexPost(ctx, created.ID); err != nil {
			return err
		}
		events = postEvents(refOf(created), live.KindPostCreated, live.KindCommentCreated)
		recipients, err := tx.RoomMemberRecipients(ctx, room.ID, actor.UserID)
		if err != nil {
			return err
		}
		mentions, mentioned, err := p.syncMentions(ctx, tx, room, created.ID, clean.Format, clean.Body, recipients)
		if err != nil {
			return err
		}
		// The author follows their post, and so does everyone it names.
		if err := tx.AutoFollow(ctx, created.ID, append([]uint64{actor.UserID}, mentionedByName(mentions)...)); err != nil {
			return err
		}
		details := map[string]any{
			"format":      string(clean.Format),
			"length":      utf8.RuneCountInString(clean.Body),
			"tags":        clean.Tags,
			"attachments": len(in.Uploads),
		}
		if len(tasks) > 0 {
			details["tasks"] = len(tasks)
		}
		if err := recordAudit(ctx, tx, auditEntry{
			ActorID:    actor.UserID,
			Action:     ActionPostCreated,
			RoomID:     room.ID,
			EntityType: EntityPost,
			EntityID:   created.ID,
			Summary:    who + " posted in #" + room.Name,
			Details:    details,
		}); err != nil {
			return err
		}
		var assigned []assignment
		if err := addNewTasks(ctx, tx, actor, who, room, created, tasks, names, &events, &assigned); err != nil {
			return err
		}
		notified, err := notify(ctx, tx, actor, room, created.ID,
			append(assignedNotices(assigned), mentionNoticesOf(mentioned)...))
		if err != nil {
			return err
		}
		events = append(events, notified...)
		// The members whose choice for the room is All hear of the post, but
		// not twice: those who were notified of it have the notification.
		events = append(events, postRingEvents(recipients, room.ID, created.ID, notified)...)
		return nil
	})
	if err != nil {
		p.cleaner.discard(ctx, p.st, in.Uploads)
		return Post{}, err
	}
	publishAll(p.pub, events)
	return p.viewOne(ctx, actor, room, created.ID)
}

// Reply writes a comment under the post or comment parentID, in the thread of
// the same top-level post, and its audit event post.created with the entity
// type comment (the event never holds the body). Only members of the room
// reply: a room that the actor may not read, a missing parent and a parent
// that is deleted fail with TagNotFound, a visible room that the actor has
// not joined with TagForbidden. It fails with TagInvalidInput, with all the
// problems in the metadata "fields", for input that breaks a rule. in.Uploads
// become the attachments of the comment; when Reply fails for any reason,
// their files are removed from disk. It saves the mentions and notifies as
// Create does, and also the author of the parent (a reply); of one user the
// notification of the highest rank is made: assigned, then reply, then
// mention (SPEC D30).
func (p *Posts) Reply(ctx context.Context, actor Actor, parentID uint64, in PostInput) (Post, error) {
	var created store.Post
	var room store.Room
	var events []live.Event
	err := p.st.Tx(ctx, func(tx *store.Store) error {
		events = nil
		parent, err := tx.PostByID(ctx, parentID)
		if err != nil {
			return err
		}
		room, err = loadReadableRoom(ctx, tx, actor, parent.RoomID)
		if err != nil {
			return err
		}
		if parent.DeletedAt != nil {
			return errors.New(TagNotFound, "the post is deleted", "reason", "deleted", "postID", parentID)
		}
		member, err := isMember(ctx, tx, actor.UserID, room.ID)
		if err != nil {
			return err
		}
		if !member {
			return errors.New(TagForbidden, "only members comment",
				"userID", actor.UserID, "roomID", room.ID)
		}
		clean, fields := checkPostInput(in, len(in.Uploads))
		tasks, names, err := checkTasks(ctx, tx, room.ID, in.Tasks, nil, fields)
		if err != nil {
			return err
		}
		if err := p.checkUploads(ctx, tx, fields, in.Uploads, 0); err != nil {
			return err
		}
		if len(fields) > 0 {
			return errors.New(TagInvalidInput, "the comment has mistakes", "fields", fields)
		}
		who, err := displayNameOf(ctx, tx, actor.UserID)
		if err != nil {
			return err
		}
		rootID := parent.ID
		if parent.RootID != nil {
			rootID = *parent.RootID
		}
		created = store.Post{
			RoomID: room.ID, AuthorID: actor.UserID, ParentID: &parent.ID, RootID: &rootID,
			Format: clean.Format, Body: clean.Body,
		}
		if err := tx.CreatePost(ctx, &created); err != nil {
			return err
		}
		if err := tx.ReplacePostTags(ctx, created.ID, clean.Tags); err != nil {
			return err
		}
		if err := tx.CreateAttachments(ctx, attachmentRows(created.ID, actor.UserID, in.Uploads)); err != nil {
			return err
		}
		if err := tx.ReindexPost(ctx, created.ID); err != nil {
			return err
		}
		events = postEvents(refOf(created), live.KindPostCreated, live.KindCommentCreated)
		recipients, err := tx.RoomMemberRecipients(ctx, room.ID, actor.UserID)
		if err != nil {
			return err
		}
		mentions, mentioned, err := p.syncMentions(ctx, tx, room, created.ID, clean.Format, clean.Body, recipients)
		if err != nil {
			return err
		}
		// Whoever comments in a thread follows it, and so does everyone named.
		if err := tx.AutoFollow(ctx, rootID, append([]uint64{actor.UserID}, mentionedByName(mentions)...)); err != nil {
			return err
		}
		followers, err := tx.ThreadFollowers(ctx, rootID, actor.UserID)
		if err != nil {
			return err
		}
		details := map[string]any{
			"format":      string(clean.Format),
			"length":      utf8.RuneCountInString(clean.Body),
			"tags":        clean.Tags,
			"rootID":      rootID,
			"parentID":    parent.ID,
			"attachments": len(in.Uploads),
		}
		if len(tasks) > 0 {
			details["tasks"] = len(tasks)
		}
		if err := recordAudit(ctx, tx, auditEntry{
			ActorID:    actor.UserID,
			Action:     ActionPostCreated,
			RoomID:     room.ID,
			EntityType: EntityComment,
			EntityID:   created.ID,
			Summary:    who + " commented in #" + room.Name,
			Details:    details,
		}); err != nil {
			return err
		}
		var assigned []assignment
		if err := addNewTasks(ctx, tx, actor, who, room, created, tasks, names, &events, &assigned); err != nil {
			return err
		}
		notices := append(assignedNotices(assigned), notice{userID: parent.AuthorID, kind: NotificationReply})
		// Every follower of the thread hears of the comment as a reply, in a
		// burst of a minute as one notification (SPEC §7.15).
		for _, id := range followers {
			notices = append(notices, notice{userID: id, kind: NotificationReply, foldRoot: rootID})
		}
		notified, err := notify(ctx, tx, actor, room, created.ID, append(notices, mentionNoticesOf(mentioned)...))
		events = append(events, notified...)
		return err
	})
	if err != nil {
		p.cleaner.discard(ctx, p.st, in.Uploads)
		return Post{}, err
	}
	publishAll(p.pub, events)
	return p.viewOne(ctx, actor, room, created.ID)
}

// Edit changes the body, the format, the tags and the attachments of a post or
// comment and writes post.edited (entity type comment for a comment) with only
// what changed (for the body that it changed and its length, never the text;
// for the attachments the names added and removed). The attachments listed in
// in.KeepAttachments stay, in.Uploads are added and the others are removed,
// their files from disk after the change is committed; when Edit fails for
// any reason, the files of in.Uploads are removed from disk. It does nothing,
// and writes no event, when nothing changed. Only the author may: another reader gets TagForbidden. A room that
// the actor may not read, a missing post and a deleted post fail with
// TagNotFound. It fails with TagInvalidInput, with all the problems in the
// metadata "fields", for input that breaks a rule. When the body or the format
// changed it rebuilds the mentions and notifies only the users newly
// mentioned, and it notifies the users newly assigned to a task of the post
// (not those who stay on it).
func (p *Posts) Edit(ctx context.Context, actor Actor, postID uint64, in PostInput) (Post, error) {
	var room store.Room
	var removedKeys []string
	var events []live.Event
	err := p.st.Tx(ctx, func(tx *store.Store) error {
		removedKeys = nil
		events = nil
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
		current, err := tx.AttachmentsOfPosts(ctx, []uint64{row.ID})
		if err != nil {
			return err
		}
		kept, removed, unknown := splitAttachments(current[row.ID], in.KeepAttachments)
		clean, fields := checkPostInput(in, len(kept)+len(in.Uploads))
		if unknown {
			fields[FieldAttachments] = messageUploadAgain
		}
		taskRows, err := tx.TaskRowsOfPosts(ctx, []uint64{row.ID})
		if err != nil {
			return err
		}
		tasks, names, err := checkTasks(ctx, tx, room.ID, in.Tasks, tasksByID(taskRows[row.ID]), fields)
		if err != nil {
			return err
		}
		if err := p.checkUploads(ctx, tx, fields, in.Uploads, len(kept)); err != nil {
			return err
		}
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
		if len(in.Uploads) > 0 || len(removed) > 0 {
			added := make([]string, len(in.Uploads))
			for i, u := range in.Uploads {
				added[i] = cleanFileName(u.FileName)
			}
			gone := make([]string, len(removed))
			for i, a := range removed {
				gone[i] = a.FileName
			}
			details["attachments"] = map[string]any{"added": added, "removed": gone}
		}
		plan := planTasks(taskRows[row.ID], tasks, names)
		if !plan.empty() {
			details["tasks"] = plan.planDetails()
		}
		if len(details) == 0 {
			return nil
		}
		who, err := displayNameOf(ctx, tx, actor.UserID)
		if err != nil {
			return err
		}
		entity, noun := entityOf(row)
		if err := tx.UpdatePostContent(ctx, row.ID, clean.Format, clean.Body, p.clk.Now()); err != nil {
			return err
		}
		if tagsChanged {
			if err := tx.ReplacePostTags(ctx, row.ID, clean.Tags); err != nil {
				return err
			}
		}
		if err := tx.CreateAttachments(ctx, attachmentRows(row.ID, actor.UserID, in.Uploads)); err != nil {
			return err
		}
		removedIDs := make([]uint64, len(removed))
		for i, a := range removed {
			removedIDs[i] = a.ID
		}
		if err := tx.DeleteAttachments(ctx, removedIDs); err != nil {
			return err
		}
		if err := tx.ReindexPost(ctx, row.ID); err != nil {
			return err
		}
		removedKeys = storageKeys(removed)
		events = postEvents(refOf(row), live.KindPostUpdated, live.KindCommentUpdated)
		var mentioned []store.MentionedUser
		if bodyChanged || formatChanged {
			recipients, err := tx.RoomMemberRecipients(ctx, room.ID, actor.UserID)
			if err != nil {
				return err
			}
			var mentions []store.MentionedUser
			if mentions, mentioned, err = p.syncMentions(ctx, tx, room, row.ID, clean.Format, clean.Body, recipients); err != nil {
				return err
			}
			if err := tx.AutoFollow(ctx, followRootOf(row), mentionedByName(mentions)); err != nil {
				return err
			}
		}
		if err := recordAudit(ctx, tx, auditEntry{
			ActorID:    actor.UserID,
			Action:     ActionPostEdited,
			RoomID:     room.ID,
			EntityType: entity,
			EntityID:   row.ID,
			Summary:    who + " edited a " + noun + " in #" + room.Name,
			Details:    details,
		}); err != nil {
			return err
		}
		var assigned []assignment
		if !plan.empty() {
			audits, err := applyTasks(ctx, tx, actor, who, room, row, plan, names, &assigned)
			if err != nil {
				return err
			}
			events = append(events, boardEvent(room.ID, row.ID))
			if err := recordAudits(ctx, tx, audits); err != nil {
				return err
			}
		}
		notified, err := notify(ctx, tx, actor, room, row.ID,
			append(assignedNotices(assigned), mentionNoticesOf(mentioned)...))
		events = append(events, notified...)
		return err
	})
	if err != nil {
		p.cleaner.discard(ctx, p.st, in.Uploads)
		return Post{}, err
	}
	publishAll(p.pub, events)
	p.cleaner.remove(ctx, removedKeys)
	return p.viewOne(ctx, actor, room, postID)
}

// Delete deletes a post or comment and writes post.deleted (entity type
// comment for a comment) with {"kept": bool}. The
// author or an admin who can read the room may; another reader gets
// TagForbidden, and a room that the actor may not read, a missing post and a
// deleted post fail with TagNotFound. A post or comment with replies stays as a
// deleted placeholder with no body, tags, reactions, mentions or attachments; one
// without replies disappears with everything on it (D21). When a comment
// disappears and its parent is a deleted placeholder with no other replies, the
// parent disappears too, and so on up the thread, up to the top-level post;
// those cleanups write no events. The files of the attachments that go are
// removed from disk after the change is committed. The notifications of the
// post and of its tasks are deleted with it, a kept placeholder included.
func (p *Posts) Delete(ctx context.Context, actor Actor, postID uint64) error {
	var keys []string
	var events []live.Event
	err := p.st.Tx(ctx, func(tx *store.Store) error {
		keys = nil
		events = nil
		rootGone := uint64(0)
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
		entity, noun := entityOf(row)
		kept, err := tx.PostHasReplies(ctx, row.ID)
		if err != nil {
			return err
		}
		own, err := tx.AttachmentsOfPosts(ctx, []uint64{row.ID})
		if err != nil {
			return err
		}
		keys = storageKeys(own[row.ID])
		// The tasks of the post leave the board, each with its own event, the
		// kept placeholder included (D21). They go first: the events read them.
		taskRows, err := tx.TaskRowsOfPosts(ctx, []uint64{row.ID})
		if err != nil {
			return err
		}
		taskAudits, err := applyTasks(ctx, tx, actor, who, room, row, taskPlan{remove: taskRows[row.ID]}, nil, nil)
		if err != nil {
			return err
		}
		if kept {
			if err := tx.DeletePostExtras(ctx, row.ID); err != nil {
				return err
			}
			if err := tx.MarkPostDeleted(ctx, row.ID, p.clk.Now()); err != nil {
				return err
			}
			if err := tx.ReindexPost(ctx, row.ID); err != nil {
				return err
			}
		} else {
			if err := tx.DeletePost(ctx, row.ID); err != nil {
				return err
			}
			if err := tx.ReindexPost(ctx, row.ID); err != nil {
				return err
			}
			chainKeys, goneRoot, err := removeEmptyPlaceholders(ctx, tx, row.ParentID)
			if err != nil {
				return err
			}
			keys = append(keys, chainKeys...)
			rootGone = goneRoot
		}
		events = postEvents(refOf(row), live.KindPostDeleted, live.KindCommentDeleted)
		if rootGone != 0 {
			// The comment took its thread with it: the top-level post is gone
			// too, so the room hears post.deleted instead of post.updated.
			events = append(events[:1],
				live.Event{Topic: live.RoomTopic(room.ID), Kind: live.KindPostDeleted, RoomID: room.ID, PostID: rootGone},
				live.Event{Topic: live.PostTopic(rootGone), Kind: live.KindPostDeleted, RoomID: room.ID, PostID: rootGone})
		}
		if row.ParentID == nil {
			// A top-level post leaves the unread counts of the other members
			// (SPEC §7.17); a comment is not counted.
			ids, err := tx.RoomMemberIDs(ctx, room.ID, row.AuthorID)
			if err != nil {
				return err
			}
			for _, id := range ids {
				events = append(events, unreadEvent(id, room.ID))
			}
		}
		if err := recordAudit(ctx, tx, auditEntry{
			ActorID:    actor.UserID,
			Action:     ActionPostDeleted,
			RoomID:     room.ID,
			EntityType: entity,
			EntityID:   row.ID,
			Summary:    who + " deleted a " + noun + " in #" + room.Name,
			Details:    map[string]any{"kept": kept},
		}); err != nil {
			return err
		}
		if len(taskAudits) > 0 {
			events = append(events, boardEvent(room.ID, row.ID))
		}
		return recordAudits(ctx, tx, taskAudits)
	})
	if err != nil {
		return err
	}
	publishAll(p.pub, events)
	p.cleaner.remove(ctx, keys)
	return nil
}

// Feed returns a page of the top-level posts of a room that the actor may
// read, newest first. beforeID 0 starts from the newest post; another value
// returns the posts older than it. limit is the page size: 30 when it is 0 or
// less, at most 100. A deleted post that has replies appears as deleted. For a
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

// Get returns a post or comment of a room that the actor may read; a deleted
// one that was kept comes back with Deleted set. A missing post and a room that the
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

// Thread returns the top-level post and all the comments of the thread that
// postID belongs to: postID is the top-level post, or any comment of the
// thread (Focus is then that comment's ID). The comments come depth first,
// siblings oldest first. A missing post and a room that the actor may not read
// fail with TagNotFound.
func (p *Posts) Thread(ctx context.Context, actor Actor, postID uint64) (Thread, error) {
	row, err := p.st.PostByID(ctx, postID)
	if err != nil {
		return Thread{}, err
	}
	room, err := loadReadableRoom(ctx, p.st, actor, row.RoomID)
	if err != nil {
		return Thread{}, err
	}
	root, focus := row, uint64(0)
	if row.RootID != nil {
		focus = row.ID
		root, err = p.st.PostByID(ctx, *row.RootID)
		if err != nil {
			return Thread{}, err
		}
	}
	rows, err := p.st.ThreadComments(ctx, root.ID)
	if err != nil {
		return Thread{}, err
	}
	order := threadOrder(root.ID, rows)

	// One query for the tags of the whole thread; the count of the root is
	// made from the rows.
	ids := make([]uint64, 0, len(rows)+1)
	ids = append(ids, root.ID)
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	tags, err := p.st.TagNamesOfPosts(ctx, ids)
	if err != nil {
		return Thread{}, err
	}
	atts, err := p.st.AttachmentsOfPosts(ctx, ids)
	if err != nil {
		return Thread{}, err
	}
	reacts, err := p.st.ReactionsOfPosts(ctx, ids)
	if err != nil {
		return Thread{}, err
	}
	tasks, err := p.st.TaskRowsOfPosts(ctx, ids)
	if err != nil {
		return Thread{}, err
	}
	mentions, err := p.st.MentionedUsernamesOfPosts(ctx, ids)
	if err != nil {
		return Thread{}, err
	}
	member, err := isMember(ctx, p.st, actor.UserID, room.ID)
	if err != nil {
		return Thread{}, err
	}
	following, err := p.st.FollowingPosts(ctx, actor.UserID, []uint64{root.ID})
	if err != nil {
		return Thread{}, err
	}
	count := 0
	for _, r := range rows {
		if r.DeletedAt == nil {
			count++
		}
	}
	extras := postExtras{tags: tags, atts: atts, reacts: reacts, tasks: tasks, mentions: mentions, member: member, following: following}
	rootExtras := extras
	rootExtras.counts = map[uint64]int{root.ID: count}
	rootView, err := p.buildViews(actor, room, []store.Post{root}, rootExtras)
	if err != nil {
		return Thread{}, err
	}
	views, err := p.buildViews(actor, room, rows, extras)
	if err != nil {
		return Thread{}, err
	}
	byID := make(map[uint64]int, len(rows))
	for i, r := range rows {
		byID[r.ID] = i
	}
	comments := make([]Comment, len(order))
	for i, n := range order {
		view := views[byID[n.id]]
		c := Comment{Post: view, Depth: n.depth, ParentID: view.ParentID, ParentAuthor: rootView[0].Author}
		if j, ok := byID[view.ParentID]; ok {
			c.ParentAuthor = views[j].Author
		}
		comments[i] = c
	}
	return Thread{Root: rootView[0], Comments: comments, Focus: focus}, nil
}

// SuggestTags returns up to 10 tag names that start with prefix (normalized
// like a tag), used on posts in rooms that the actor can read, ordered by
// name. An empty prefix suggests the first names.
func (p *Posts) SuggestTags(ctx context.Context, actor Actor, prefix string) ([]string, error) {
	prefix = strings.ToLower(strings.TrimLeft(strings.TrimSpace(prefix), "#"))
	return p.st.SuggestTagNames(ctx, actor.UserID, prefix, tagSuggestLimit)
}

// Preview renders a body the way a post would show it, for the preview of the
// editor. Any logged-in actor may preview; an actor with no ID fails with
// TagUnauthenticated. It fails with TagInvalidInput, with the problems in the
// metadata "fields", for a format that is neither text nor markdown, a body
// that is not valid text and a body longer than 20,000 characters. An empty
// body previews as an empty result. A mention is shown as written, not as a
// link: only the mentions a post has saved become links.
func (p *Posts) Preview(_ context.Context, actor Actor, format Format, body string) (template.HTML, error) {
	if actor.UserID == 0 {
		return "", errors.New(TagUnauthenticated, "a preview needs a login", "reason", "none")
	}
	fields := map[string]string{}
	if format != FormatText && format != FormatMarkdown {
		fields[FieldFormat] = "Choose text or markdown."
	}
	switch {
	case !utf8.ValidString(body):
		fields[FieldBody] = "Use valid text."
	case utf8.RuneCountInString(body) > postBodyMaxLength:
		fields[FieldBody] = "Use at most 20,000 characters."
	}
	if len(fields) > 0 {
		return "", errors.New(TagInvalidInput, "the preview has mistakes", "fields", fields)
	}
	body = strings.TrimRightFunc(body, unicode.IsSpace)
	if body == "" {
		return "", nil
	}
	return p.md.Render(markdown.Format(format), body)
}

// followRootOf is the top-level post of the thread that row belongs to: the
// post that is followed.
func followRootOf(row store.Post) uint64 {
	if row.RootID != nil {
		return *row.RootID
	}
	return row.ID
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
// of rows, with eight queries: the tags, the attachments, the comment counts,
// the reactions, the tasks (two), the mentions and the membership of the actor. The rows have Author loaded.
func (p *Posts) postViews(ctx context.Context, st *store.Store, actor Actor, room store.Room, rows []store.Post) ([]Post, error) {
	ids := make([]uint64, len(rows))
	for i, row := range rows {
		ids[i] = row.ID
	}
	tags, err := st.TagNamesOfPosts(ctx, ids)
	if err != nil {
		return nil, err
	}
	atts, err := st.AttachmentsOfPosts(ctx, ids)
	if err != nil {
		return nil, err
	}
	counts, err := st.CommentCounts(ctx, ids)
	if err != nil {
		return nil, err
	}
	reacts, err := st.ReactionsOfPosts(ctx, ids)
	if err != nil {
		return nil, err
	}
	tasks, err := st.TaskRowsOfPosts(ctx, ids)
	if err != nil {
		return nil, err
	}
	mentions, err := st.MentionedUsernamesOfPosts(ctx, ids)
	if err != nil {
		return nil, err
	}
	member, err := isMember(ctx, st, actor.UserID, room.ID)
	if err != nil {
		return nil, err
	}
	followIDs := ids
	for _, row := range rows {
		if row.RootID != nil {
			followIDs = append(followIDs, *row.RootID)
		}
	}
	following, err := st.FollowingPosts(ctx, actor.UserID, followIDs)
	if err != nil {
		return nil, err
	}
	return p.buildViews(actor, room, rows, postExtras{tags: tags, atts: atts, counts: counts, reacts: reacts, tasks: tasks, mentions: mentions, member: member, following: following})
}

// postExtras is what hangs on a set of posts of one room, read by the caller
// of buildViews.
type postExtras struct {
	// tags, atts, reacts and tasks are the tags, attachments, reactions and
	// tasks of each post, by post ID.
	tags   map[uint64][]string
	atts   map[uint64][]store.Attachment
	reacts map[uint64][]store.ReactionRow
	tasks  map[uint64][]store.TaskRow
	// mentions are the usernames each post mentions, by post ID: the saved
	// mentions, which the body shows as links.
	mentions map[uint64][]string
	// counts are the comment counts of the posts.
	counts map[uint64]int
	// member is true when the actor is a member of the room.
	member bool
	// following is the set of the top-level posts the actor follows.
	following map[uint64]bool
}

// buildViews makes the views of posts of one room for the actor, in the order
// of rows, from what the caller has read. The rows have Author loaded.
func (p *Posts) buildViews(actor Actor, room store.Room, rows []store.Post, x postExtras) ([]Post, error) {
	views := make([]Post, len(rows))
	today := dayOf(p.clk.Now())
	for i, row := range rows {
		v := Post{
			ID:           row.ID,
			RoomID:       room.ID,
			RoomName:     room.Name,
			Format:       row.Format,
			Tags:         []string{},
			Attachments:  []AttachmentView{},
			Tasks:        []TaskCard{},
			CommentCount: x.counts[row.ID],
			Reactions:    []ReactionCount{},
			IsComment:    row.ParentID != nil,
			CreatedAt:    row.CreatedAt,
			EditedAt:     row.EditedAt,
			Deleted:      row.DeletedAt != nil,
		}
		if row.ParentID != nil {
			v.ParentID = *row.ParentID
		}
		if row.RootID != nil {
			v.RootID = *row.RootID
		}
		if !v.Deleted {
			v.Body = row.Body
			v.Tags = nonNil(x.tags[row.ID])
			v.Attachments = attachmentViews(x.atts[row.ID])
			v.Tasks = taskCards(x.tasks[row.ID], today)
			v.Reactions = reactionCounts(x.reacts[row.ID], actor.UserID)
			v.CanReact = x.member
			v.Following = x.following[followRootOf(row)]
			if row.Author != nil {
				v.Author = UserRef{ID: row.Author.ID, Username: row.Author.Username, DisplayName: row.Author.DisplayName, AvatarKey: row.Author.AvatarKey}
			}
			var err error
			v.HTML, err = p.md.RenderMentions(markdown.Format(row.Format), row.Body, x.mentions[row.ID])
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

// threadNode is a comment in the order of a thread.
type threadNode struct {
	id    uint64
	depth int
}

// threadOrder puts the comments of the thread of the post rootID in reading
// order: depth first, every comment before its replies, siblings oldest first.
// rows are the comments of the thread, oldest first. A row whose parent is not
// in the thread is left out.
func threadOrder(rootID uint64, rows []store.Post) []threadNode {
	children := make(map[uint64][]uint64, len(rows))
	for _, r := range rows {
		if r.ParentID != nil {
			children[*r.ParentID] = append(children[*r.ParentID], r.ID)
		}
	}
	order := make([]threadNode, 0, len(rows))
	// The stack holds the nodes still to visit; the oldest sibling is on top.
	stack := make([]threadNode, 0, len(rows))
	push := func(parentID uint64, depth int) {
		kids := children[parentID]
		for i := len(kids) - 1; i >= 0; i-- {
			stack = append(stack, threadNode{id: kids[i], depth: depth})
		}
	}
	push(rootID, 1)
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		order = append(order, n)
		push(n.id, n.depth+1)
	}
	return order
}

// entityOf returns the audit entity type of a row and what to call it in the
// summary of an event: a post, or a comment when it has a parent.
func entityOf(row store.Post) (EntityType, string) {
	if row.ParentID != nil {
		return EntityComment, "comment"
	}
	return EntityPost, "post"
}

// removeEmptyPlaceholders removes, from the comment parentID upwards, every
// ancestor that is a deleted placeholder with no replies left (D21), up to the
// top-level post. It stops at the first ancestor that is not deleted or still
// has replies. A nil parentID does nothing. It returns the storage keys of the
// attachments of the posts it removed (a placeholder has none left, but the
// rows are the authority), for the caller to remove from disk after the
// commit, and the ID of the top-level post when that was removed too (0
// otherwise).
func removeEmptyPlaceholders(ctx context.Context, tx *store.Store, parentID *uint64) ([]string, uint64, error) {
	var keys []string
	var goneRoot uint64
	for parentID != nil {
		parent, err := tx.PostByID(ctx, *parentID)
		if err != nil {
			return nil, 0, err
		}
		if parent.DeletedAt == nil {
			return keys, goneRoot, nil
		}
		hasReplies, err := tx.PostHasReplies(ctx, parent.ID)
		if err != nil {
			return nil, 0, err
		}
		if hasReplies {
			return keys, goneRoot, nil
		}
		atts, err := tx.AttachmentsOfPosts(ctx, []uint64{parent.ID})
		if err != nil {
			return nil, 0, err
		}
		keys = append(keys, storageKeys(atts[parent.ID])...)
		if err := tx.DeletePost(ctx, parent.ID); err != nil {
			return nil, 0, err
		}
		if err := tx.ReindexPost(ctx, parent.ID); err != nil {
			return nil, 0, err
		}
		if parent.ParentID == nil {
			goneRoot = parent.ID
		}
		parentID = parent.ParentID
	}
	return keys, goneRoot, nil
}

// splitAttachments divides the attachments of a post into those that stay
// (their IDs are in keep) and those that go. unknown is true when keep holds
// an ID that is not an attachment of the post.
func splitAttachments(current []store.Attachment, keep []uint64) (kept, removed []store.Attachment, unknown bool) {
	want := make(map[uint64]bool, len(keep))
	for _, id := range keep {
		want[id] = true
	}
	for _, a := range current {
		if want[a.ID] {
			kept = append(kept, a)
			delete(want, a.ID)
		} else {
			removed = append(removed, a)
		}
	}
	return kept, removed, len(want) > 0
}

// checkPostInput applies the rules of SPEC §7.3 and §7.7 to in; attachments is
// the number of attachments the post has when the call is done, and a post
// with at least one, or with a task, may have an empty body. It returns the
// input as it is stored (body without trailing whitespace, tags normalized and
// without duplicates) and the problems as field name to message; the problems are
// empty when all rules hold.
func checkPostInput(in PostInput, attachments int) (PostInput, map[string]string) {
	fields := map[string]string{}
	clean := PostInput{Format: in.Format}
	if in.Format != FormatText && in.Format != FormatMarkdown {
		fields[FieldFormat] = "Choose text or markdown."
	}

	clean.Body = strings.TrimRightFunc(in.Body, unicode.IsSpace)
	switch {
	case !utf8.ValidString(clean.Body):
		fields[FieldBody] = "Use valid text."
	case clean.Body == "" && attachments == 0 && len(in.Tasks) == 0:
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
