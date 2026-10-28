package api

import "github.com/rm4n0s/clowder/internal/app"

// callJSON is a call in the answers of the API (SPEC §7.18).
type callJSON struct {
	ID             uint64      `json:"id"`
	ConversationID uint64      `json:"conversation_id"`
	Caller         userRefJSON `json:"caller"`
	Callee         userRefJSON `json:"callee"`
	Video          bool        `json:"video"`
	State          string      `json:"state"`
	CreatedAt      string      `json:"created_at"`
	AnsweredAt     *string     `json:"answered_at"`
	EndedAt        *string     `json:"ended_at"`
	EndReason      *string     `json:"end_reason"`
	// EndedBy is the ID of the user who ended the call; null when it ended on
	// its own (a timeout, a failure, a disabled account).
	EndedBy *uint64 `json:"ended_by"`
	// Incoming is true when the user who asks is the callee.
	Incoming bool `json:"incoming"`
}

// callOf makes the JSON of a call.
func callOf(c app.Call) callJSON {
	out := callJSON{
		ID:             c.ID,
		ConversationID: c.ConversationID,
		Caller:         userRefOf(c.Caller),
		Callee:         userRefOf(c.Callee),
		Video:          c.Video,
		State:          string(c.State),
		CreatedAt:      rfc3339(c.CreatedAt),
		Incoming:       c.Incoming,
	}
	if c.AnsweredAt != nil {
		t := rfc3339(*c.AnsweredAt)
		out.AnsweredAt = &t
	}
	if c.EndedAt != nil {
		t := rfc3339(*c.EndedAt)
		out.EndedAt = &t
	}
	if c.EndReason != "" {
		reason := string(c.EndReason)
		out.EndReason = &reason
	}
	if c.EndedBy != 0 {
		by := c.EndedBy
		out.EndedBy = &by
	}
	return out
}

// callEntryJSON is the call of a call entry in the history of a conversation:
// a message of kind "call" (SPEC §7.18).
type callEntryJSON struct {
	ID              uint64 `json:"id"`
	Video           bool   `json:"video"`
	Outcome         string `json:"outcome"`
	DurationSeconds int    `json:"duration_seconds"`
	// Text is the line the pages show for the entry to the user who asks.
	Text string `json:"text"`
}

// callEntryOf makes the JSON of the call of a call entry.
func callEntryOf(e app.CallEntry) callEntryJSON {
	return callEntryJSON{ID: e.CallID, Video: e.Video, Outcome: string(e.Outcome), DurationSeconds: e.DurationSeconds, Text: e.Text}
}
