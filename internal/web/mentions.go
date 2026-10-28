package web

import (
	"context"
	"net/http"
	"slices"
	"strings"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/web/views"
)

// confirmGroupMentionField is the form value (or query) that says the person
// answered "Send" to the question of a group mention.
const confirmGroupMentionField = "confirm_group_mention"

// groupMentionToConfirm tells how many people a group mention of body would
// notify when the page has to ask before it sends it (SPEC §7.15): the count
// is above 5 and the person did not confirm yet. It returns 0 when nothing is
// to be asked. The count comes from the service, with the presence for @here.
// A failed count is not an answer here: the write that follows reports the
// same failure.
func (s *Server) groupMentionToConfirm(r *http.Request, body string, count func() (int, error)) int {
	if r.FormValue(confirmGroupMentionField) == "1" || !strings.Contains(body, "@") {
		return 0
	}
	n, err := count()
	if err != nil {
		s.logger.Debug("counting the group mention failed", "pattern", r.Pattern, "err", err)
		return 0
	}
	if n <= app.GroupMentionConfirmAbove {
		return 0
	}
	return n
}

// confirmOfEditor is the place of the question of an editor.
func confirmOfEditor(d views.PostEditorData) views.GroupMentionConfirmData {
	return views.GroupMentionConfirmData{ID: d.ID, Count: d.ConfirmCount, SendPath: views.GroupMentionSendPath(d.Action)}
}

// onlineSet returns the set of those among userIDs who are online now, for the
// dots of a members list. The person who looks at the page counts as online:
// their stream opens right after the page. Presence that cannot be read says
// that nobody else is online.
func (s *Server) onlineSet(ctx context.Context, actor app.Actor, userIDs []uint64) map[uint64]bool {
	online := map[uint64]bool{}
	for id, ok := range s.svc.Presence.OnlineIn(ctx, userIDs) {
		online[id] = ok
	}
	if actor.UserID != 0 && slices.Contains(userIDs, actor.UserID) {
		online[actor.UserID] = true
	}
	return online
}
