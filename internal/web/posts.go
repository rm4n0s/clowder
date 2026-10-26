package web

import (
	"net/http"
	"regexp"
	"strconv"

	"github.com/a-h/templ"
	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/web/views"
	"github.com/rm4n0s/errors"
	"github.com/starfederation/datastar-go/datastar"
)

// editorIDPattern is what the ID of an editor, which the preview and the
// suggestions send back, must look like. The editors make theirs of letters,
// digits and "-" ("new", "edit-12").
var editorIDPattern = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)

// defaultEditorID is the ID of the editor of a new post.
const defaultEditorID = "new"

// patchOp is one element patch of an SSE answer: a component with the options
// that say where it goes, or the removal of the elements a selector finds.
type patchOp struct {
	component templ.Component
	options   []datastar.PatchElementOption
	remove    string
}

// patchAll answers a Datastar request with an SSE stream of the patches, in
// order. A write that fails ends the stream.
func (s *Server) patchAll(w http.ResponseWriter, r *http.Request, ops ...patchOp) {
	sse := datastar.NewSSE(w, r)
	for _, op := range ops {
		var err error
		if op.remove != "" {
			err = sse.RemoveElement(op.remove)
		} else {
			err = sse.PatchElementTempl(op.component, op.options...)
		}
		if err != nil {
			appErr := errors.NewErr(tagSSEWriteFailed, err).SetMetadata("event", "patch-elements")
			s.logger.Debug("sse write failed", "err", appErr)
			return
		}
	}
}

// beforeOf reads the query "before": the ID of the post after which a page of
// the feed starts. A missing or wrong value is 0, which starts at the newest.
func beforeOf(r *http.Request) uint64 {
	before, err := strconv.ParseUint(r.URL.Query().Get("before"), 10, 64)
	if err != nil {
		return 0
	}
	return before
}

// fromOf reads the value "from" of the request, from the query or the form:
// views.FromPost when the post is shown on its own page, else "".
func fromOf(r *http.Request) string {
	if r.FormValue("from") == views.FromPost {
		return views.FromPost
	}
	return ""
}

// editorIDOf reads the ID of the editor that sent the request (the value or
// query "editor"); an ID that does not look like one is the new post's.
func editorIDOf(r *http.Request) string {
	id := r.FormValue("editor")
	if !editorIDPattern.MatchString(id) {
		return defaultEditorID
	}
	return id
}

// postInputFrom reads what the editor sent.
func postInputFrom(r *http.Request) app.PostInput {
	return app.PostInput{
		Format: app.Format(r.PostFormValue("format")),
		Body:   r.PostFormValue("body"),
		Tags:   app.ParseTags(r.PostFormValue("tags")),
	}
}

// typedInto puts what the person typed in the form back into the editor.
func typedInto(d views.PostEditorData, r *http.Request) views.PostEditorData {
	d.Format = r.PostFormValue("format")
	d.Body = r.PostFormValue("body")
	d.Tags = r.PostFormValue("tags")
	return d
}

// postID reads the ID of the post from the path. When it is not a number it has
// answered with the 404 page and returns false.
func (s *Server) postID(w http.ResponseWriter, r *http.Request) (uint64, bool) {
	id, err := strconv.ParseUint(r.PathValue("id"), 10, 64)
	if err != nil || id == 0 {
		s.notFound(w, r)
		return 0, false
	}
	return id, true
}

// postFail answers a failed read of a post: the error page, except that a
// Datastar request for a post that is gone or hidden is sent to the page of the
// post, which says so (the client drops the body of a 404).
func (s *Server) postFail(w http.ResponseWriter, r *http.Request, err error, id uint64) {
	if isDatastar(r) && isTag(err, app.TagNotFound) {
		s.redirect(w, r, views.PostPath(id), http.StatusSeeOther)
		return
	}
	s.fail(w, r, err)
}

// roomFeed answers GET /rooms/{id}/feed?before=<id>: the posts older than that
// post, which the feed appends, and the new end of the feed (the sentinel of the
// next page, or nothing). A request that is not from Datastar is sent to the
// page of the room that shows the same posts.
func (s *Server) roomFeed(w http.ResponseWriter, r *http.Request) {
	id, ok := s.roomID(w, r)
	if !ok {
		return
	}
	before := beforeOf(r)
	if !isDatastar(r) {
		target := views.RoomPath(id)
		if before != 0 {
			target = views.OlderPostsPath(id, before)
		}
		http.Redirect(w, r, target, http.StatusSeeOther)
		return
	}
	actor, _ := actorFrom(r.Context())
	page, err := s.svc.Posts.Feed(r.Context(), actor, id, before, 0)
	if err != nil {
		s.roomFail(w, r, err, id)
		return
	}
	d := views.FeedData{RoomID: id, Page: page, Before: before}
	s.patchAll(w, r,
		patchOp{
			component: views.FeedPosts(d),
			options:   []datastar.PatchElementOption{datastar.WithSelector("#feed-posts"), datastar.WithModeAppend()},
		},
		// Replaced, not morphed, so that the new sentinel is a new element and
		// Datastar watches it afresh.
		patchOp{
			component: views.FeedMore(d),
			options:   []datastar.PatchElementOption{datastar.WithSelector("#feed-more"), datastar.WithModeReplace()},
		},
	)
}

// postCreate answers POST /rooms/{id}/posts with the form values "body",
// "format" and "tags". A Datastar request gets the new post at the top of the
// feed and a fresh editor; a plain post is sent back to the room. A mistake
// shows the problems in the editor and keeps what was typed.
func (s *Server) postCreate(w http.ResponseWriter, r *http.Request) {
	id, ok := s.roomID(w, r)
	if !ok {
		return
	}
	if !s.parseBodyForm(w, r) {
		return
	}
	actor, _ := actorFrom(r.Context())
	post, err := s.svc.Posts.Create(r.Context(), actor, id, postInputFrom(r))
	if err != nil {
		s.postCreateFailed(w, r, err, id)
		return
	}
	if !isDatastar(r) {
		http.Redirect(w, r, views.RoomPath(id), http.StatusSeeOther)
		return
	}
	s.patchAll(w, r,
		patchOp{
			component: views.PostCard(views.PostCardData{Post: post}),
			options:   []datastar.PatchElementOption{datastar.WithSelector("#feed-posts"), datastar.WithModePrepend()},
		},
		// Replaced, not morphed: a morph leaves the text the person typed in
		// the fields, and the editor must be empty again.
		patchOp{
			component: views.PostEditor(views.NewPostEditorData(id)),
			options:   []datastar.PatchElementOption{datastar.WithSelector("#post-editor-new"), datastar.WithModeReplace()},
		},
	)
}

// postCreateFailed shows the problems of a post that was not written. A room
// that is gone or hidden is the 404 page.
func (s *Server) postCreateFailed(w http.ResponseWriter, r *http.Request, err error, id uint64) {
	if isTag(err, app.TagNotFound) {
		s.roomFail(w, r, err, id)
		return
	}
	// A plain post gets the whole page back, so it needs the room and the feed.
	var base views.RoomData
	if !isDatastar(r) && !isTag(err, app.TagUnauthenticated) {
		var ok bool
		if base, ok = s.roomPageData(w, r, id, 0); !ok {
			return
		}
	}
	s.formFailed(w, r, err, func(problems views.FormErrors) formView {
		editor := typedInto(views.NewPostEditorData(id), r)
		editor.Errors = problems
		d := base
		d.Editor = editor
		if !d.Room.IsMember {
			// A person who has not joined has no editor to show it in.
			d.Message = problems.Form
		}
		return formView{fragment: views.PostEditor(editor), page: views.RoomPage(d)}
	})
}

// postPageData reads a post for the actor and makes the data of its page. When
// a read fails it has answered (see postFail) and returns false.
func (s *Server) postPageData(w http.ResponseWriter, r *http.Request, id uint64) (views.PostPageData, bool) {
	actor, _ := actorFrom(r.Context())
	post, err := s.svc.Posts.Get(r.Context(), actor, id)
	if err != nil {
		s.postFail(w, r, err, id)
		return views.PostPageData{}, false
	}
	sb, ok := s.sidebarFor(w, r, post.RoomID)
	if !ok {
		return views.PostPageData{}, false
	}
	return views.PostPageData{Sidebar: sb, Post: post}, true
}

// postPage answers GET /posts/{id}: the page of the post, for a person who may
// read its room, else the 404 page.
func (s *Server) postPage(w http.ResponseWriter, r *http.Request) {
	id, ok := s.postID(w, r)
	if !ok {
		return
	}
	d, ok := s.postPageData(w, r, id)
	if !ok {
		return
	}
	s.render(w, r, http.StatusOK, views.PostPage(d))
}

// postEditForm answers GET /posts/{id}/edit: the post replaced by its editor,
// filled with its body, format and tags. A browser without JavaScript gets the
// page of the post with the editor in it.
func (s *Server) postEditForm(w http.ResponseWriter, r *http.Request) {
	id, ok := s.postID(w, r)
	if !ok {
		return
	}
	actor, _ := actorFrom(r.Context())
	post, err := s.svc.Posts.Get(r.Context(), actor, id)
	if err == nil && post.Deleted {
		err = errors.New(app.TagNotFound, "the post is deleted", "reason", "deleted", "postID", id)
	}
	if err == nil && !post.CanEdit {
		err = errors.New(app.TagForbidden, "only the author edits a post", "userID", actor.UserID, "postID", id)
	}
	if err != nil {
		s.postActionFailed(w, r, err, id)
		return
	}
	editor := views.EditPostEditorData(post, fromOf(r))
	if isDatastar(r) {
		s.patch(w, r, views.PostEdit(views.PostEditData{Post: post, Editor: editor}))
		return
	}
	d, ok := s.postPageData(w, r, id)
	if !ok {
		return
	}
	d.Editing, d.Editor = true, editor
	s.render(w, r, http.StatusOK, views.PostPage(d))
}

// postView answers GET /posts/{id}/view, which the Cancel of the editor asks
// for: the post as it is, in place of the editor.
func (s *Server) postView(w http.ResponseWriter, r *http.Request) {
	id, ok := s.postID(w, r)
	if !ok {
		return
	}
	if !isDatastar(r) {
		http.Redirect(w, r, views.PostPath(id), http.StatusSeeOther)
		return
	}
	actor, _ := actorFrom(r.Context())
	post, err := s.svc.Posts.Get(r.Context(), actor, id)
	if err != nil {
		s.postFail(w, r, err, id)
		return
	}
	s.patch(w, r, views.PostCard(views.PostCardData{Post: post, From: fromOf(r)}))
}

// postEditSave answers POST /posts/{id}/edit with the form values "body",
// "format" and "tags": the post is saved and shown again in place of the
// editor. A mistake shows the problems in the editor and keeps what was typed.
func (s *Server) postEditSave(w http.ResponseWriter, r *http.Request) {
	id, ok := s.postID(w, r)
	if !ok {
		return
	}
	if !s.parseBodyForm(w, r) {
		return
	}
	from := fromOf(r)
	actor, _ := actorFrom(r.Context())
	post, err := s.svc.Posts.Edit(r.Context(), actor, id, postInputFrom(r))
	if err == nil {
		if !isDatastar(r) {
			http.Redirect(w, r, views.PostPath(id), http.StatusSeeOther)
			return
		}
		s.patch(w, r, views.PostCard(views.PostCardData{Post: post, From: from}))
		return
	}
	if isTag(err, app.TagNotFound) {
		s.postFail(w, r, err, id)
		return
	}
	// A plain post gets the whole page back.
	var page views.PostPageData
	if !isDatastar(r) && !isTag(err, app.TagUnauthenticated) {
		if page, ok = s.postPageData(w, r, id); !ok {
			return
		}
	}
	s.formFailed(w, r, err, func(problems views.FormErrors) formView {
		editor := typedInto(views.EditPostEditorData(app.Post{ID: id}, from), r)
		editor.Errors = problems
		d := page
		d.Editing, d.Editor = true, editor
		return formView{
			fragment: views.PostEdit(views.PostEditData{Post: app.Post{ID: id}, Editor: editor}),
			page:     views.PostPage(d),
		}
	})
}

// postDelete answers POST /posts/{id}/delete. A post without replies is gone:
// the feed drops it, and its own page sends the person to the room. A post with
// replies stays as the placeholder "This post was deleted." that replaces it. A
// plain post is sent to the room.
func (s *Server) postDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := s.postID(w, r)
	if !ok {
		return
	}
	if !s.parseForm(w, r) {
		return
	}
	from := fromOf(r)
	actor, _ := actorFrom(r.Context())
	// Read first, to know the room the person goes back to.
	before, err := s.svc.Posts.Get(r.Context(), actor, id)
	if err != nil {
		s.postFail(w, r, err, id)
		return
	}
	if err := s.svc.Posts.Delete(r.Context(), actor, id); err != nil {
		s.postActionFailed(w, r, err, id)
		return
	}
	if !isDatastar(r) {
		http.Redirect(w, r, views.RoomPath(before.RoomID), http.StatusSeeOther)
		return
	}
	// A post with replies is still there, as a placeholder.
	kept, err := s.svc.Posts.Get(r.Context(), actor, id)
	switch {
	case err == nil:
		s.patch(w, r, views.PostCard(views.PostCardData{Post: kept, From: from}))
	case !isTag(err, app.TagNotFound):
		s.fail(w, r, err)
	case from == views.FromPost:
		s.redirect(w, r, views.RoomPath(before.RoomID), http.StatusSeeOther)
	default:
		s.patchAll(w, r, patchOp{remove: "#post-" + strconv.FormatUint(id, 10)})
	}
}

// postActionFailed shows the problem of a failed action on a post (edit,
// delete): a post that is gone or hidden is the 404 page, any other problem is
// a message above the post, patched in place for a Datastar request (status
// 200) and in the whole page, with the status of statusOf, for a plain one.
func (s *Server) postActionFailed(w http.ResponseWriter, r *http.Request, err error, id uint64) {
	status := statusOf(err)
	if status == http.StatusFound {
		s.redirectToLogin(w, r)
		return
	}
	if isTag(err, app.TagNotFound) {
		s.postFail(w, r, err, id)
		return
	}
	var message string
	switch {
	case isTag(err, app.TagInvalidInput):
		message = messageActionInvalid
	case isTag(err, app.TagConflict):
		message = messageConflict
	case isTag(err, app.TagForbidden):
		message = messageForbidden
	default:
		s.logFailure(r, err)
		message = messageTryAgain
	}
	d, ok := s.postPageData(w, r, id)
	if !ok {
		return
	}
	d.Message = message
	if isDatastar(r) {
		s.patch(w, r, views.PostCard(views.PostCardData{Post: d.Post, From: fromOf(r), Message: message}))
		return
	}
	s.render(w, r, status, views.PostPage(d))
}

// postPreview answers POST /preview with the form values "editor", "format" and
// "body": the box of that editor, patched with the body rendered the way a post
// shows it (or the reason it cannot be, such as a body that is too long). Any
// logged-in person may preview.
func (s *Server) postPreview(w http.ResponseWriter, r *http.Request) {
	if !s.parseBodyForm(w, r) {
		return
	}
	actor, _ := actorFrom(r.Context())
	html, err := s.svc.Posts.Preview(r.Context(), actor, app.Format(r.PostFormValue("format")), r.PostFormValue("body"))
	data := views.PostPreviewData{EditorID: editorIDOf(r), HTML: html}
	switch {
	case err == nil:
	case isTag(err, app.TagUnauthenticated):
		s.redirectToLogin(w, r)
		return
	case isTag(err, app.TagInvalidInput):
		fields := formErrorsOf(err).Fields
		data.Message = firstNonEmpty(fields[app.FieldBody], fields[app.FieldFormat], messageInvalid)
	default:
		s.logFailure(r, err)
		data.Message = messageTryAgain
	}
	s.patch(w, r, views.PostPreview(data))
}

// tagSuggest answers GET /tags/suggest?editor=<id>&q=<the tag being typed>: the
// list under the tags field of that editor, patched with the tags that start
// with q, from the posts the person may read. An empty q gives an empty list.
func (s *Server) tagSuggest(w http.ResponseWriter, r *http.Request) {
	actor, _ := actorFrom(r.Context())
	editor := editorIDOf(r)
	var tags []string
	q := r.URL.Query().Get("q")
	// Only the last piece counts: the person is typing that tag. A separator
	// at the end means they are between two tags.
	if pieces := app.ParseTags(q); len(pieces) > 0 && len(app.ParseTags(q+"x")) == len(pieces) {
		var err error
		tags, err = s.svc.Posts.SuggestTags(r.Context(), actor, pieces[len(pieces)-1])
		if err != nil {
			// The list stays empty; the person can still type the tag.
			s.logFailure(r, err)
			tags = nil
		}
	}
	s.patch(w, r, views.TagSuggestions(editor, tags))
}
