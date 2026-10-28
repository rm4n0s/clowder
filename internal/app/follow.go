package app

import (
	"context"

	"github.com/rm4n0s/clowder/internal/store"
	"github.com/rm4n0s/errors"
)

// Follow makes the actor follow a post, so that every comment in its thread
// notifies them with *Threads I follow* (SPEC §7.15). On a comment it acts on
// the top-level post of its thread. It is idempotent, and it brings back a
// follow that the actor removed. It writes no audit event (a follow is not
// among the actions of §7.10) and publishes nothing. A post that the actor may
// not read, a missing post and a deleted one fail with TagNotFound; a disabled
// or missing account fails with TagUnauthenticated. A visible room can be
// read, and so its posts followed, without being a member.
func (p *Posts) Follow(ctx context.Context, actor Actor, postID uint64) error {
	return p.setFollow(ctx, actor, postID, true)
}

// Unfollow makes the actor stop following a post. It is Follow the other way,
// with the same rules: on a comment it acts on its top-level post, it is
// idempotent, and an automatic follow (writing in the thread, being mentioned)
// does not bring the follow back. The comments of a thread that the actor
// started or commented in still notify them as direct replies when they answer
// the actor's post or comment.
func (p *Posts) Unfollow(ctx context.Context, actor Actor, postID uint64) error {
	return p.setFollow(ctx, actor, postID, false)
}

// setFollow is Follow and Unfollow.
func (p *Posts) setFollow(ctx context.Context, actor Actor, postID uint64, following bool) error {
	return p.st.Tx(ctx, func(tx *store.Store) error {
		if _, err := checkActive(ctx, tx, actor); err != nil {
			return err
		}
		row, err := tx.PostByID(ctx, postID)
		if err != nil {
			return err
		}
		if _, err := loadReadableRoom(ctx, tx, actor, row.RoomID); err != nil {
			return err
		}
		root := row
		if row.RootID != nil {
			if root, err = tx.PostByID(ctx, *row.RootID); err != nil {
				return err
			}
		}
		if root.DeletedAt != nil {
			return errors.New(TagNotFound, "the post is deleted", "reason", "deleted", "postID", postID)
		}
		return tx.SetFollow(ctx, actor.UserID, root.ID, following)
	})
}
