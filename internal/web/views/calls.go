package views

import (
	"strconv"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
)

// CallPath returns the path of the call of a direct conversation, or of one of
// its actions: CallPath(id, "") is the path that starts a call, and the
// actions are "answer", "decline", "cancel" and "hangup" (SPEC §7.18).
func CallPath(conversationID uint64, action string) string {
	p := ConversationPath(conversationID) + "/call"
	if action != "" {
		p += "/" + action
	}
	return p
}

// CallAnswerQuery is the query a page of a conversation gets after its user
// answered a call: "call=video" or "call=audio". The page then opens the call,
// sending video or not as the value says (the choice is not stored by the
// server).
const CallAnswerQuery = "call"

// CallAnsweredPath returns the page a user is sent to when they answer a call:
// the conversation, which opens the call with what they chose to send.
func CallAnsweredPath(conversationID uint64, video bool) string {
	way := "audio"
	if video {
		way = "video"
	}
	return ConversationPath(conversationID) + "?" + CallAnswerQuery + "=" + way
}

// CallSocketURL returns the path of the signalling socket of the call of a
// conversation, /calls/<id>/ws.
func CallSocketURL(conversationID uint64) string {
	return "/calls/" + strconv.FormatUint(conversationID, 10) + "/ws"
}

// CallInviteData is what the ringing dialog shows: the call that rings for the
// user, and how it rings.
type CallInviteData struct {
	// CallID and ConversationID are the call and its direct conversation.
	CallID         uint64
	ConversationID uint64
	// Caller is the person who calls.
	Caller app.UserRef
	// Video is true when the caller started the call with video.
	Video bool
	// Ring is true when the ringtone plays (SPEC §7.15); Browser is true when
	// the browser notification is wanted.
	Ring    bool
	Browser bool
	// Text is the text of the browser notification.
	Text string
}

// CallInviteOf makes the data of the dialog out of an invitation.
func CallInviteOf(inv app.CallInvitation) *CallInviteData {
	return &CallInviteData{
		CallID: inv.Call.ID, ConversationID: inv.Call.ConversationID, Caller: inv.Call.Caller, Video: inv.Call.Video,
		Ring: inv.Ring, Browser: inv.Browser, Text: inv.Text,
	}
}

// CallKindText is "Video call" or "Audio call".
func CallKindText(video bool) string {
	if video {
		return "Video call"
	}
	return "Audio call"
}

// CallData is what the page of a direct conversation shows of its calls: the
// buttons that start one, and the call that is in progress.
type CallData struct {
	// ConversationID is the conversation.
	ConversationID uint64
	// Enabled is true for a direct conversation of a server whose calls are
	// on; only then the page has the buttons and the call area.
	Enabled bool
	// Open is the call that rings or is active; nil for none. The buttons give
	// way to it.
	Open *CallPanelData
}

// CallPanelData is the call in progress, as the user of the page sees it.
type CallPanelData struct {
	// CallID is the call.
	CallID uint64
	// Video is true when the caller started the call with video.
	Video bool
	// Active is true once the callee answered; false while it rings.
	Active bool
	// Incoming is true when the user of the page is the callee.
	Incoming bool
	// Other is the other person of the call.
	Other app.UserRef
	// Elapsed is how long the call has been active, as the server read it.
	Elapsed time.Duration
	// Answer is what the user chose to send when they answered ("video" or
	// "audio"), from the query of the page; empty when they did not answer in
	// this page load. It makes the tab the one that takes part in the call.
	Answer string
}

// callRole is "caller" or "callee".
func (p CallPanelData) callRole() string {
	if p.Incoming {
		return "callee"
	}
	return "caller"
}

// callState is "ringing" or "active".
func (p CallPanelData) callState() string {
	if p.Active {
		return "active"
	}
	return "ringing"
}

// callSendsVideo says whether the page sends video when the call opens: the
// callee what they chose, the caller what they started the call with.
func (p CallPanelData) callSendsVideo() bool {
	if p.Incoming && p.Answer != "" {
		return p.Answer == "video"
	}
	return p.Video
}

// callElapsedSeconds is Elapsed in whole seconds.
func (p CallPanelData) callElapsedSeconds() string {
	return strconv.Itoa(max(0, int(p.Elapsed/time.Second)))
}

// callDurationText writes a duration as mm:ss, or h:mm:ss from an hour.
func callDurationText(d time.Duration) string {
	s := max(0, int(d/time.Second))
	if s >= 3600 {
		return strconv.Itoa(s/3600) + ":" + twoDigits(s%3600/60) + ":" + twoDigits(s%60)
	}
	return twoDigits(s/60) + ":" + twoDigits(s%60)
}

func twoDigits(n int) string {
	if n < 10 {
		return "0" + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}

// boolText is "true" or "false", for the data attributes call.js reads.
func boolText(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
