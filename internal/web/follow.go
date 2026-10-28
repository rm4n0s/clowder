package web

import (
	"net/http"

	"github.com/rm4n0s/clowder/internal/web/views"
)

// postFollow answers POST /posts/{id}/follow: the person follows the post, so
// that every comment in its thread notifies them (SPEC §7.15). A Datastar
// request gets the post again with the button "Unfollow"; a plain post is sent
// to the post.
func (s *Server) postFollow(w http.ResponseWriter, r *http.Request) {
	s.postFollowing(w, r, true)
}

// postUnfollow answers POST /posts/{id}/unfollow: the person stops following
// the post. A Datastar request gets the post again with the button "Follow";
// a plain post is sent to the post.
func (s *Server) postUnfollow(w http.ResponseWriter, r *http.Request) {
	s.postFollowing(w, r, false)
}

// postFollowing is postFollow and postUnfollow.
func (s *Server) postFollowing(w http.ResponseWriter, r *http.Request, following bool) {
	id, ok := s.postID(w, r)
	if !ok {
		return
	}
	if !s.parseForm(w, r) {
		return
	}
	actor, _ := actorFrom(r.Context())
	var err error
	if following {
		err = s.svc.Posts.Follow(r.Context(), actor, id)
	} else {
		err = s.svc.Posts.Unfollow(r.Context(), actor, id)
	}
	if err != nil {
		s.postActionFailed(w, r, err, id)
		return
	}
	if !isDatastar(r) {
		http.Redirect(w, r, views.PostPath(id), http.StatusSeeOther)
		return
	}
	post, err := s.svc.Posts.Get(r.Context(), actor, id)
	if err != nil {
		s.postFail(w, r, err, id)
		return
	}
	card, ok := s.cardFor(w, r, post, fromOf(r), "")
	if !ok {
		return
	}
	s.patch(w, r, views.PostContent(card))
}
