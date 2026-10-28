package store

// allModels lists every model for AutoMigrate, a table after the tables that
// its foreign keys point to.
func allModels() []any {
	return []any{
		&User{},
		&UserSettings{},
		&Invitation{},
		&PasswordReset{},
		&SSHKey{},
		&SSHCode{},
		&PendingLogin{},
		&Session{},
		&Room{},
		&RoomMember{},
		&RoomRead{},
		&Post{},
		&Tag{},
		&PostTag{},
		&Attachment{},
		&Reaction{},
		&Mention{},
		&BoardColumn{},
		&Task{},
		&TaskAssignee{},
		&Notification{},
		&Conversation{},
		&Call{},
		&Message{},
		&ConversationMember{},
		&PlacePref{},
		&PostFollow{},
		&MessageAttachment{},
		&MessageReaction{},
		&AuditEvent{},
	}
}
