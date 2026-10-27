package store

import (
	"context"

	"github.com/rm4n0s/errors"
)

// CreateAttachments inserts the rows and sets their IDs and CreatedAt (the
// time of the Store's clock). No rows is a no-op. It fails with TagConflict
// when a storage key is already used by another attachment and with
// TagQueryFailed when a post or an uploader does not exist.
func (s *Store) CreateAttachments(ctx context.Context, rows []Attachment) error {
	if len(rows) == 0 {
		return nil
	}
	err := s.db(ctx).Create(&rows).Error
	if err != nil {
		switch classify(err) {
		case failureConflict:
			return errors.NewErr(TagConflict, err).SetMetadata("op", "createAttachments")
		default:
			return errors.NewErr(TagQueryFailed, err).SetMetadata("op", "createAttachments")
		}
	}
	return nil
}

// AttachmentByID returns the attachment with the given ID. It fails with
// TagNotFound when there is none.
func (s *Store) AttachmentByID(ctx context.Context, id uint64) (Attachment, error) {
	var a Attachment
	err := s.db(ctx).First(&a, id).Error
	if err != nil {
		switch classify(err) {
		case failureNotFound:
			return Attachment{}, errors.NewErr(TagNotFound, err).SetMetadata("op", "attachmentByID", "attachmentID", id)
		default:
			return Attachment{}, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "attachmentByID", "attachmentID", id)
		}
	}
	return a, nil
}

// AttachmentsOfPosts returns the attachments of each of the posts, oldest
// first (by ID), with one query. A post without attachments has no key in the
// map.
func (s *Store) AttachmentsOfPosts(ctx context.Context, postIDs []uint64) (map[uint64][]Attachment, error) {
	out := map[uint64][]Attachment{}
	if len(postIDs) == 0 {
		return out, nil
	}
	var rows []Attachment
	err := s.db(ctx).Where("post_id IN ?", postIDs).Order("id ASC").Find(&rows).Error
	if err != nil {
		return nil, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "attachmentsOfPosts")
	}
	for _, a := range rows {
		out[a.PostID] = append(out[a.PostID], a)
	}
	return out, nil
}

// DeleteAttachments deletes the rows with these IDs. No IDs is a no-op; an ID
// that does not exist is ignored.
func (s *Store) DeleteAttachments(ctx context.Context, ids []uint64) error {
	if len(ids) == 0 {
		return nil
	}
	if err := s.db(ctx).Where("id IN ?", ids).Delete(&Attachment{}).Error; err != nil {
		return errors.NewErr(TagQueryFailed, err).SetMetadata("op", "deleteAttachments")
	}
	return nil
}

// StorageKeysInUse returns those of the keys that some attachment row, of a
// post or of a message, uses, in no particular order.
func (s *Store) StorageKeysInUse(ctx context.Context, keys []string) ([]string, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	var used []string
	err := s.db(ctx).Model(&Attachment{}).Where("storage_key IN ?", keys).Pluck("storage_key", &used).Error
	if err != nil {
		return nil, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "storageKeysInUse")
	}
	var ofMessages []string
	err = s.db(ctx).Model(&MessageAttachment{}).Where("storage_key IN ?", keys).Pluck("storage_key", &ofMessages).Error
	if err != nil {
		return nil, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "storageKeysInUse")
	}
	return append(used, ofMessages...), nil
}

// AttachmentKeysOfRoom returns the storage keys of the attachments of every
// post and comment of the room, deleted placeholders included.
func (s *Store) AttachmentKeysOfRoom(ctx context.Context, roomID uint64) ([]string, error) {
	var keys []string
	err := s.db(ctx).Model(&Attachment{}).
		Joins("JOIN posts ON posts.id = attachments.post_id").
		Where("posts.room_id = ?", roomID).
		Pluck("attachments.storage_key", &keys).Error
	if err != nil {
		return nil, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "attachmentKeysOfRoom", "roomID", roomID)
	}
	return keys, nil
}
