package views

// GroupMentionConfirmData is what the question of a group mention shows.
type GroupMentionConfirmData struct {
	// ID is the ID of the editor or composer the question belongs to.
	ID string
	// Count is the number of people the mention would notify; 0 shows nothing.
	Count int
	// SendPath is where Send posts to: the action of the form with the query
	// confirm_group_mention=1.
	SendPath string
}

// Question is the text of the question: "This will notify 14 people. Send
// anyway?".
func (d GroupMentionConfirmData) Question() string {
	return "This will notify " + groupMentionCountText(d.Count) + " people. Send anyway?"
}

// GroupMentionConfirmID is the id of the place of the question of the editor or
// composer id.
func GroupMentionConfirmID(id string) string {
	return "group-mention-confirm-" + id
}

// GroupMentionSendPath is the address the form with the action posts to when
// the person confirmed the group mention.
func GroupMentionSendPath(action string) string {
	return action + "?confirm_group_mention=1"
}
