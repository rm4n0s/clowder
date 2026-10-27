package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/errors"
)

const (
	// feedMaxLimit is the largest page of the feed a client may ask for.
	feedMaxLimit = 100
	// messageLimitInvalid is the problem with the query "limit".
	messageLimitInvalid = "Use a number from 1 to 100."
	// messageBeforeInvalid is the problem with the query "before".
	messageBeforeInvalid = "Use the ID of a post."
	// messageUploadsUnavailable is the problem with upload_ids until the
	// uploads exist.
	messageUploadsUnavailable = "Uploading files is not available yet."
	// messageKeepOnCreate is the problem with attachment_ids on a new post.
	messageKeepOnCreate = "A new post has no attachments to keep. Use upload_ids."
)

// taskRequest is a task in the body of a post.
type taskRequest struct {
	ID          uint64   `json:"id"`
	Title       string   `json:"title"`
	AssigneeIDs []uint64 `json:"assignee_ids"`
	DueDate     *string  `json:"due_date"`
}

// postRequest is the body of a new post, a new comment and an edit of either.
// Every field may be left out. A new post or comment takes what is there and
// leaves the rest empty (the format is text). An edit keeps what is not
// there (or null) and replaces what is: tags, tasks and attachment_ids as the
// lists given.
type postRequest struct {
	Format        *string        `json:"format"`
	Body          *string        `json:"body"`
	Tags          *[]string      `json:"tags"`
	Tasks         *[]taskRequest `json:"tasks"`
	UploadIDs     []string       `json:"upload_ids"`
	AttachmentIDs *[]uint64      `json:"attachment_ids"`
}

// postRoutes are the routes of the posts, comments, reactions and tags.
func (s *Server) postRoutes() []Route {
	return []Route{
		s.route(http.MethodGet, "/rooms/{id}/posts", false, s.feed),
		s.route(http.MethodPost, "/rooms/{id}/posts", false, s.createPost),
		s.route(http.MethodGet, "/posts/{id}", false, s.post),
		s.route(http.MethodGet, "/posts/{id}/thread", false, s.thread),
		s.route(http.MethodPost, "/posts/{id}/comments", false, s.createComment),
		s.route(http.MethodPatch, "/posts/{id}", false, s.patchPost),
		s.route(http.MethodDelete, "/posts/{id}", false, s.deletePost),
		s.route(http.MethodPut, "/posts/{id}/reactions/{emoji}", false, s.putReaction),
		s.route(http.MethodDelete, "/posts/{id}/reactions/{emoji}", false, s.deleteReaction),
		s.route(http.MethodGet, "/tags", false, s.tags),
	}
}

// feedParams reads the queries "before" and "limit" of the feed. A missing
// before starts at the newest post and a missing limit is the default page;
// anything else that is not a positive number, or a limit above 100, fails
// with tagBeforeInvalid or tagLimitInvalid.
func feedParams(r *http.Request) (before uint64, limit int, err error) {
	query := r.URL.Query()
	if text := query.Get("before"); query.Has("before") {
		before, err = strconv.ParseUint(text, 10, 64)
		if err != nil || before == 0 {
			return 0, 0, errors.New(tagBeforeInvalid, "before is not the ID of a post",
				"fields", map[string]string{"before": messageBeforeInvalid})
		}
	}
	if text := query.Get("limit"); query.Has("limit") {
		n, convErr := strconv.Atoi(text)
		if convErr != nil || n < 1 || n > feedMaxLimit {
			return 0, 0, errors.New(tagLimitInvalid, "limit is not a number from 1 to 100",
				"fields", map[string]string{"limit": messageLimitInvalid})
		}
		limit = n
	}
	return before, limit, nil
}

// feed answers GET /rooms/{id}/posts?before=&limit=: a page of the top-level
// posts of the room, newest first, 30 by default and 100 at most. next_before
// is the before of the next page, null on the last.
func (s *Server) feed(w http.ResponseWriter, r *http.Request) {
	c, _ := callerFrom(r.Context())
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	before, limit, err := feedParams(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	page, err := s.svc.Posts.Feed(r.Context(), c.actor, id, before, limit)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	out := feedResponse{Posts: make([]postJSON, len(page.Posts))}
	for i, p := range page.Posts {
		out.Posts[i] = postOf(p)
	}
	if page.HasNext {
		next := page.NextBefore
		out.NextBefore = &next
	}
	s.writeJSON(w, r, http.StatusOK, out)
}

// postInput makes the input of the service from a body. current is the post
// an edit changes: what the body leaves out is taken from it. For a new post
// or comment current is nil and what the body leaves out is empty.
func postInput(in postRequest, current *app.Post) (app.PostInput, error) {
	out := app.PostInput{Format: app.FormatText}
	if current != nil {
		out.Format, out.Body, out.Tags = current.Format, current.Body, current.Tags
		out.Tasks = make([]app.TaskInput, len(current.Tasks))
		for i, t := range current.Tasks {
			task := app.TaskInput{ID: t.ID, Title: t.Title, DueDate: t.DueDate}
			for _, a := range t.Assignees {
				task.Assignees = append(task.Assignees, a.ID)
			}
			out.Tasks[i] = task
		}
		for _, a := range current.Attachments {
			out.KeepAttachments = append(out.KeepAttachments, a.ID)
		}
	}
	if in.Format != nil {
		out.Format = app.Format(*in.Format)
	}
	if in.Body != nil {
		out.Body = *in.Body
	}
	if in.Tags != nil {
		out.Tags = *in.Tags
	}
	if in.Tasks != nil {
		out.Tasks = make([]app.TaskInput, len(*in.Tasks))
		for i, t := range *in.Tasks {
			task := app.TaskInput{ID: t.ID, Title: t.Title, Assignees: t.AssigneeIDs}
			if t.DueDate != nil && *t.DueDate != "" {
				due, err := time.Parse(dateLayout, *t.DueDate)
				if err != nil {
					return app.PostInput{}, errors.New(tagDueDateInvalid, "a task has a due date that is not a day",
						"fields", map[string]string{app.FieldTasks: "Task " + strconv.Itoa(i+1) + ": write the due date as YYYY-MM-DD."},
						"row", i+1)
				}
				task.DueDate = &due
			}
			out.Tasks[i] = task
		}
	}
	if in.AttachmentIDs != nil {
		if current == nil && len(*in.AttachmentIDs) > 0 {
			return app.PostInput{}, errors.New(tagKeepOnCreate, "a new post has no attachments to keep",
				"fields", map[string]string{"attachment_ids": messageKeepOnCreate})
		}
		out.KeepAttachments = *in.AttachmentIDs
	}
	if len(in.UploadIDs) > 0 {
		// TODO(step 16.3): turn the IDs that POST /uploads returns into
		// app.Uploads and put them in out.Uploads.
		return app.PostInput{}, errors.New(tagUploadsUnavailable, "uploads are not available yet",
			"fields", map[string]string{"upload_ids": messageUploadsUnavailable})
	}
	return out, nil
}

// createPost answers POST /rooms/{id}/posts: a member writes a post.
func (s *Server) createPost(w http.ResponseWriter, r *http.Request) {
	c, _ := callerFrom(r.Context())
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	var body postRequest
	if err := decodeJSON(w, r, &body); err != nil {
		s.writeError(w, r, err)
		return
	}
	in, err := postInput(body, nil)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	post, err := s.svc.Posts.Create(r.Context(), c.actor, id, in)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeCreatedPost(w, r, post)
}

// createComment answers POST /posts/{id}/comments: a member replies to a post
// or a comment.
func (s *Server) createComment(w http.ResponseWriter, r *http.Request) {
	c, _ := callerFrom(r.Context())
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	var body postRequest
	if err := decodeJSON(w, r, &body); err != nil {
		s.writeError(w, r, err)
		return
	}
	in, err := postInput(body, nil)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	post, err := s.svc.Posts.Reply(r.Context(), c.actor, id, in)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeCreatedPost(w, r, post)
}

// writeCreatedPost answers 201 with the post and its Location.
func (s *Server) writeCreatedPost(w http.ResponseWriter, r *http.Request, post app.Post) {
	w.Header().Set("Location", Prefix+"/posts/"+strconv.FormatUint(post.ID, 10))
	s.writeJSON(w, r, http.StatusCreated, postOf(post))
}

// post answers GET /posts/{id}: a post or a comment of a room the caller may
// read. One that was deleted and stays for its replies comes back with
// deleted true.
func (s *Server) post(w http.ResponseWriter, r *http.Request) {
	c, _ := callerFrom(r.Context())
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	post, err := s.svc.Posts.Get(r.Context(), c.actor, id)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, postOf(post))
}

// thread answers GET /posts/{id}/thread: the top-level post and every comment
// under it, depth first, oldest sibling first, each with its depth. The id may
// be a comment: the thread of its post comes back, with focus_id naming it.
func (s *Server) thread(w http.ResponseWriter, r *http.Request) {
	c, _ := callerFrom(r.Context())
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	th, err := s.svc.Posts.Thread(r.Context(), c.actor, id)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	out := threadResponse{Post: postOf(th.Root), Comments: make([]threadCommentJSON, len(th.Comments))}
	for i, comment := range th.Comments {
		out.Comments[i] = threadCommentJSON{Depth: comment.Depth, Comment: postOf(comment.Post)}
	}
	if th.Focus != 0 {
		focus := th.Focus
		out.FocusID = &focus
	}
	s.writeJSON(w, r, http.StatusOK, out)
}

// patchPost answers PATCH /posts/{id}: the author changes what the body
// holds of the post or comment, and gets it back.
func (s *Server) patchPost(w http.ResponseWriter, r *http.Request) {
	c, _ := callerFrom(r.Context())
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	var body postRequest
	if err := decodeJSON(w, r, &body); err != nil {
		s.writeError(w, r, err)
		return
	}
	current, err := s.svc.Posts.Get(r.Context(), c.actor, id)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	in, err := postInput(body, &current)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	post, err := s.svc.Posts.Edit(r.Context(), c.actor, id, in)
	if err != nil {
		// The attachments of an edit are the attachment_ids it keeps.
		s.writeError(w, r, renameField(err, app.FieldAttachments, "attachment_ids"))
		return
	}
	s.writeJSON(w, r, http.StatusOK, postOf(post))
}

// deletePost answers DELETE /posts/{id}: the author or an admin deletes a post
// or comment.
func (s *Server) deletePost(w http.ResponseWriter, r *http.Request) {
	c, _ := callerFrom(r.Context())
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.svc.Posts.Delete(r.Context(), c.actor, id); err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeNoContent(w)
}

// putReaction answers PUT /posts/{id}/reactions/{emoji}: a member puts the
// emoji on the post or comment. Putting one that is there is not an error. The
// answer is the reactions of the post.
func (s *Server) putReaction(w http.ResponseWriter, r *http.Request) {
	s.setReaction(w, r, true)
}

// deleteReaction answers DELETE /posts/{id}/reactions/{emoji}: a member takes
// the emoji off. Taking off one that is not there is not an error. The answer
// is the reactions of the post.
func (s *Server) deleteReaction(w http.ResponseWriter, r *http.Request) {
	s.setReaction(w, r, false)
}

// setReaction is putReaction and deleteReaction.
func (s *Server) setReaction(w http.ResponseWriter, r *http.Request, present bool) {
	c, _ := callerFrom(r.Context())
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	got, err := s.svc.Posts.SetReaction(r.Context(), c.actor, id, r.PathValue("emoji"), present)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, reactionsResponse{Reactions: reactionsOf(got)})
}

// tags answers GET /tags?prefix=: up to 10 tags used in the rooms the caller
// can read that start with prefix, by name.
func (s *Server) tags(w http.ResponseWriter, r *http.Request) {
	c, _ := callerFrom(r.Context())
	names, err := s.svc.Posts.SuggestTags(r.Context(), c.actor, r.URL.Query().Get("prefix"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if names == nil {
		names = []string{}
	}
	s.writeJSON(w, r, http.StatusOK, tagsResponse{Tags: names})
}

const (
	tagBeforeInvalid      = "BeforeInvalid"
	tagLimitInvalid       = "LimitInvalid"
	tagDueDateInvalid     = "DueDateInvalid"
	tagUploadsUnavailable = "UploadsUnavailable"
	tagKeepOnCreate       = "KeepOnCreate"
)
