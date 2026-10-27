package store

import (
	"context"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rm4n0s/errors"
	"gorm.io/gorm/clause"
)

// CreatePost inserts p and sets its ID and CreatedAt (the time of the Store's
// clock). It fails with TagQueryFailed when the room, the author or the parent
// does not exist.
func (s *Store) CreatePost(ctx context.Context, p *Post) error {
	err := s.db(ctx).Create(p).Error
	if err != nil {
		switch classify(err) {
		case failureConflict:
			return errors.NewErr(TagConflict, err).SetMetadata("op", "createPost")
		default:
			return errors.NewErr(TagQueryFailed, err).SetMetadata("op", "createPost")
		}
	}
	return nil
}

// PostByID returns the post with the given ID, with Author loaded. A deleted
// post that is kept for its replies is returned too (its DeletedAt is set). It
// fails with TagNotFound when there is none.
func (s *Store) PostByID(ctx context.Context, id uint64) (Post, error) {
	var p Post
	err := s.db(ctx).Preload("Author").First(&p, id).Error
	if err != nil {
		switch classify(err) {
		case failureNotFound:
			return Post{}, errors.NewErr(TagNotFound, err).SetMetadata("op", "postByID", "postID", id)
		default:
			return Post{}, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "postByID", "postID", id)
		}
	}
	return p, nil
}

// UpdatePostContent sets the format and the body of the post id and the time
// of the edit. It fails with TagNotFound when there is no such post.
func (s *Store) UpdatePostContent(ctx context.Context, id uint64, format Format, body string, editedAt time.Time) error {
	res := s.db(ctx).Model(&Post{}).Where("id = ?", id).
		Updates(map[string]any{"format": format, "body": body, "edited_at": editedAt.UTC()})
	if res.Error != nil {
		return errors.NewErr(TagQueryFailed, res.Error).SetMetadata("op", "updatePostContent", "postID", id)
	}
	if res.RowsAffected == 0 {
		return errors.New(TagNotFound, "no post with this ID", "op", "updatePostContent", "postID", id)
	}
	return nil
}

// MarkPostDeleted keeps the post id as a deleted placeholder (D21): it sets
// DeletedAt to at and empties the body. It fails with TagNotFound when there
// is no such post.
func (s *Store) MarkPostDeleted(ctx context.Context, id uint64, at time.Time) error {
	res := s.db(ctx).Model(&Post{}).Where("id = ?", id).
		Updates(map[string]any{"deleted_at": at.UTC(), "body": ""})
	if res.Error != nil {
		return errors.NewErr(TagQueryFailed, res.Error).SetMetadata("op", "markPostDeleted", "postID", id)
	}
	if res.RowsAffected == 0 {
		return errors.New(TagNotFound, "no post with this ID", "op", "markPostDeleted", "postID", id)
	}
	return nil
}

// DeletePost deletes the row of the post id. Everything that hangs on it
// (tags, reactions, mentions, attachments, tasks and its comments) cascades.
// It fails with TagNotFound when there is no such post.
func (s *Store) DeletePost(ctx context.Context, id uint64) error {
	res := s.db(ctx).Where("id = ?", id).Delete(&Post{})
	if res.Error != nil {
		return errors.NewErr(TagQueryFailed, res.Error).SetMetadata("op", "deletePost", "postID", id)
	}
	if res.RowsAffected == 0 {
		return errors.New(TagNotFound, "no post with this ID", "op", "deletePost", "postID", id)
	}
	return nil
}

// DeletePostExtras deletes the tag links, reactions, mentions, attachments and
// notifications of the post id and leaves the post itself. It is for a post that stays as a
// deleted placeholder (D21). It does not touch the tasks of the post: the
// service deletes them one by one, to audit them and to close the gaps they
// leave in the columns.
func (s *Store) DeletePostExtras(ctx context.Context, id uint64) error {
	for _, model := range []any{&PostTag{}, &Reaction{}, &Mention{}, &Attachment{}, &Notification{}} {
		if err := s.db(ctx).Where("post_id = ?", id).Delete(model).Error; err != nil {
			return errors.NewErr(TagQueryFailed, err).SetMetadata("op", "deletePostExtras", "postID", id)
		}
	}
	return nil
}

// PostHasReplies reports whether some comment has the post id as its parent.
func (s *Store) PostHasReplies(ctx context.Context, id uint64) (bool, error) {
	var n int64
	err := s.db(ctx).Model(&Post{}).Where("parent_id = ?", id).Limit(1).Count(&n).Error
	if err != nil {
		return false, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "postHasReplies", "postID", id)
	}
	return n > 0, nil
}

// ListFeed returns the top-level posts of the room, newest first (by ID), at
// most limit of them, with Author loaded. A beforeID other than 0 keeps only
// the posts with a smaller ID. Posts kept as deleted placeholders are
// included. It uses the index idx_posts_feed.
func (s *Store) ListFeed(ctx context.Context, roomID, beforeID uint64, limit int) ([]Post, error) {
	q := s.db(ctx).Where("room_id = ? AND parent_id IS NULL", roomID)
	if beforeID != 0 {
		q = q.Where("id < ?", beforeID)
	}
	var posts []Post
	err := q.Preload("Author").Order("id DESC").Limit(limit).Find(&posts).Error
	if err != nil {
		return nil, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "listFeed", "roomID", roomID)
	}
	return posts, nil
}

// ReplacePostTags makes names the tags of the post id, in that order: the
// tags that do not exist yet are created, the links of the post are replaced.
// The names must be normalized and without duplicates.
func (s *Store) ReplacePostTags(ctx context.Context, postID uint64, names []string) error {
	if err := s.db(ctx).Where("post_id = ?", postID).Delete(&PostTag{}).Error; err != nil {
		return errors.NewErr(TagQueryFailed, err).SetMetadata("op", "replacePostTags", "postID", postID)
	}
	if len(names) == 0 {
		return nil
	}
	var existing []Tag
	if err := s.db(ctx).Where("name IN ?", names).Find(&existing).Error; err != nil {
		return errors.NewErr(TagQueryFailed, err).SetMetadata("op", "replacePostTags", "postID", postID)
	}
	byName := make(map[string]uint64, len(names))
	for _, t := range existing {
		byName[t.Name] = t.ID
	}
	var missing []Tag
	for _, name := range names {
		if _, ok := byName[name]; !ok {
			missing = append(missing, Tag{Name: name})
		}
	}
	if len(missing) > 0 {
		// In the same order whatever the order given, so that transactions that
		// make several of the same new tags cannot deadlock on PostgreSQL.
		slices.SortFunc(missing, func(a, b Tag) int { return strings.Compare(a.Name, b.Name) })
		// ON CONFLICT DO NOTHING, so that two transactions that make the same
		// new tag at once both succeed: the second waits for the first to
		// commit, then does nothing (unlike a failed INSERT, it does not abort
		// a PostgreSQL transaction). The IDs are read back by name, because a
		// row that was not inserted has none.
		if err := s.db(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&missing).Error; err != nil {
			return errors.NewErr(TagQueryFailed, err).SetMetadata("op", "replacePostTags", "postID", postID)
		}
		var made []Tag
		if err := s.db(ctx).Where("name IN ?", names).Find(&made).Error; err != nil {
			return errors.NewErr(TagQueryFailed, err).SetMetadata("op", "replacePostTags", "postID", postID)
		}
		for _, t := range made {
			byName[t.Name] = t.ID
		}
	}
	links := make([]PostTag, len(names))
	for i, name := range names {
		links[i] = PostTag{PostID: postID, TagID: byName[name]}
	}
	if err := s.db(ctx).Create(&links).Error; err != nil {
		return errors.NewErr(TagQueryFailed, err).SetMetadata("op", "replacePostTags", "postID", postID)
	}
	return nil
}

// TagNamesOfPosts returns the tag names of each of the posts, in the order
// they were given to the post, with one query. A post without tags has no key
// in the map.
func (s *Store) TagNamesOfPosts(ctx context.Context, postIDs []uint64) (map[uint64][]string, error) {
	names := map[uint64][]string{}
	if len(postIDs) == 0 {
		return names, nil
	}
	var rows []struct {
		PostID uint64
		Name   string
	}
	err := s.db(ctx).Table("post_tags").
		Select("post_tags.post_id AS post_id, tags.name AS name").
		Joins("JOIN tags ON tags.id = post_tags.tag_id").
		Where("post_tags.post_id IN ?", postIDs).
		Order("post_tags.id ASC").
		Scan(&rows).Error
	if err != nil {
		return nil, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "tagNamesOfPosts")
	}
	for _, r := range rows {
		names[r.PostID] = append(names[r.PostID], r.Name)
	}
	return names, nil
}

// CommentCounts returns the number of comments of each of the posts (the
// comments whose RootID is the post and that are not deleted), at any depth,
// with one query. A post without comments has no key in the map.
func (s *Store) CommentCounts(ctx context.Context, postIDs []uint64) (map[uint64]int, error) {
	counts := map[uint64]int{}
	if len(postIDs) == 0 {
		return counts, nil
	}
	var rows []struct {
		RootID uint64
		N      int
	}
	err := s.db(ctx).Model(&Post{}).
		Select("root_id, COUNT(*) AS n").
		Where("root_id IN ? AND deleted_at IS NULL", postIDs).
		Group("root_id").
		Scan(&rows).Error
	if err != nil {
		return nil, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "commentCounts")
	}
	for _, r := range rows {
		counts[r.RootID] = r.N
	}
	return counts, nil
}

// ThreadComments returns every comment of the thread of the post rootID, at
// any depth, oldest first (by ID), with Author loaded, with one query on the
// index idx_posts_thread. Comments kept as deleted placeholders are included.
// The post itself is not.
func (s *Store) ThreadComments(ctx context.Context, rootID uint64) ([]Post, error) {
	var comments []Post
	err := s.db(ctx).Where("root_id = ?", rootID).Preload("Author").Order("id ASC").Find(&comments).Error
	if err != nil {
		return nil, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "threadComments", "postID", rootID)
	}
	return comments, nil
}

// SuggestTagNames returns at most limit tag names that start with prefix and
// are used on a post in a room that the user may read (a visible room, or a
// room the user is a member of), ordered by name. The prefix is compared
// exactly: callers normalize it first.
func (s *Store) SuggestTagNames(ctx context.Context, userID uint64, prefix string, limit int) ([]string, error) {
	names := []string{}
	err := s.db(ctx).Raw(`SELECT DISTINCT tags.name
FROM tags
JOIN post_tags ON post_tags.tag_id = tags.id
JOIN posts ON posts.id = post_tags.post_id
JOIN rooms ON rooms.id = posts.room_id
WHERE substr(tags.name, 1, ?) = ?
  AND (rooms.visibility = ?
       OR EXISTS (SELECT 1 FROM room_members WHERE room_members.room_id = rooms.id AND room_members.user_id = ?))
ORDER BY tags.name ASC
LIMIT ?`, utf8.RuneCountInString(prefix), prefix, VisibilityVisible, userID, limit).Scan(&names).Error
	if err != nil {
		return nil, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "suggestTagNames", "userID", userID)
	}
	return names, nil
}
