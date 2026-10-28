package app

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/rm4n0s/clowder/internal/clock"
	"github.com/rm4n0s/clowder/internal/live"
	"github.com/rm4n0s/clowder/internal/store"
	"github.com/rm4n0s/errors"
)

// CallState says where a call is. It is the store's type, so the web layer can
// name the values without importing the store.
type CallState = store.CallState

// The three states of a call (SPEC §7.18).
const (
	// CallRinging is a call that the callee has not answered yet.
	CallRinging CallState = store.CallRinging
	// CallActive is a call that the callee answered and that is not over.
	CallActive CallState = store.CallActive
	// CallEnded is a call that is over.
	CallEnded CallState = store.CallEnded
)

// CallEndReason says how a call ended. It is the store's type.
type CallEndReason = store.CallEndReason

// The reasons a call ends (SPEC §7.18).
const (
	// CallEndHangup is a call that one side ended while it was active.
	CallEndHangup CallEndReason = store.CallHangup
	// CallEndCancelled is a call that the caller ended while it rang.
	CallEndCancelled CallEndReason = store.CallCancelled
	// CallEndDeclined is a call that the callee refused.
	CallEndDeclined CallEndReason = store.CallDeclined
	// CallEndMissed is a call that nobody answered in time.
	CallEndMissed CallEndReason = store.CallMissed
	// CallEndBusy is a call to someone who was in another call.
	CallEndBusy CallEndReason = store.CallBusy
	// CallEndFailed is a call whose media connection failed or was lost.
	CallEndFailed CallEndReason = store.CallFailed
	// CallEndDisabled is a call that ended because a participant was disabled.
	CallEndDisabled CallEndReason = store.CallDisabled
	// CallEndSessionEnded is a call that ended because a session of a
	// participant ended.
	CallEndSessionEnded CallEndReason = store.CallSessionEnded
	// CallEndServerStopped is a call that ended because the process holding
	// its media stopped.
	CallEndServerStopped CallEndReason = store.CallServerStopped
)

// The tags of the refusals of the calls (SPEC §7.18). The handlers answer 409
// for each of them, with the message of the metadata "fields" under FieldForm,
// which says why in words for people.
const (
	// TagCallGroup is the tag of a call in a conversation of more than two
	// people.
	TagCallGroup = "CallGroup"
	// TagCallsOff is the tag of a call on a server whose media server is off
	// (no -media-addr).
	TagCallsOff = "CallsOff"
	// TagCallCalleeDisabled is the tag of a call to a disabled user.
	TagCallCalleeDisabled = "CallCalleeDisabled"
	// TagCallCallerBusy is the tag of a call by a user who is in another call.
	TagCallCallerBusy = "CallCallerBusy"
	// TagCallCalleeBusy is the tag of a call to a user who is in another call.
	TagCallCalleeBusy = "CallCalleeBusy"
	// TagCallServerFull is the tag of a call refused because the process
	// holds as many calls as -max-calls allows.
	TagCallServerFull = "CallServerFull"
	// TagCallWrongState is the tag of a change that the call's state does not
	// allow (answering a call that is active, hanging up one that rings).
	TagCallWrongState = "CallWrongState"
	// TagCallNone is the tag of a change to the call of a conversation that
	// has none in progress.
	TagCallNone = "CallNone"
)

// IsCallRefusal reports whether tag is one of the refusals of the calls, which
// the handlers answer with 409 and a message for people.
func IsCallRefusal(tag string) bool {
	switch tag {
	case TagCallGroup, TagCallsOff, TagCallCalleeDisabled, TagCallCallerBusy, TagCallCalleeBusy,
		TagCallServerFull, TagCallWrongState, TagCallNone:
		return true
	}
	return false
}

// The messages for people of the refusals of the calls.
const (
	messageCallGroup      = "Calls are for conversations of two people."
	messageCallsOff       = "Calls are turned off on this server."
	messageCallCallerBusy = "You are in another call."
	messageCallServerFull = "Too many calls on this server right now."
	messageCallNone       = "There is no call in this conversation."
)

// callRingTimeout is how long a call rings before it is missed (SPEC §7.18).
const callRingTimeout = 45 * time.Second

// CallsConfig holds the settings of the calls service.
type CallsConfig struct {
	// ProcessID is the ID of this process, recorded as the home of the calls
	// it starts (SPEC D65): 16 hexadecimal characters, made at start. Empty
	// is only for tests that do not care.
	ProcessID string
	// Enabled is true when this process runs the media server (-media-addr is
	// set). Without it a call cannot be started.
	Enabled bool
	// MaxCalls is the most calls, ringing or active, that a process holds
	// (-max-calls). Zero or less means no limit.
	MaxCalls int
	// RingTimeout is how long a call rings before Expire ends it as missed;
	// zero means 45 seconds.
	RingTimeout time.Duration
	// Logger receives the failures that the service handles itself; nil logs
	// nothing.
	Logger *slog.Logger
}

// Calls is the service of one-to-one calls in direct conversations (SPEC
// §7.18, D63, D64). The media of a call is forwarded by the media server of
// package calls; this service holds the rules: who may call whom, the states
// of a call, the entry a finished call leaves in the conversation and the live
// events. Make it with NewCalls. It is safe for concurrent use. Only the two
// members of the direct conversation use it: anyone else, admins included,
// gets TagNotFound.
type Calls struct {
	st  *store.Store
	clk clock.Clock
	pub Publisher
	cfg CallsConfig
}

// NewCalls makes the service. pub receives the live events of the changes,
// after they are committed; nil publishes nothing.
func NewCalls(st *store.Store, clk clock.Clock, cfg CallsConfig, pub Publisher) *Calls {
	if cfg.RingTimeout <= 0 {
		cfg.RingTimeout = callRingTimeout
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	return &Calls{st: st, clk: clk, pub: orNop(pub), cfg: cfg}
}

// Call is a call as the services show it to one participant.
type Call struct {
	// ID is the ID of the call.
	ID uint64
	// ConversationID is the direct conversation of the call.
	ConversationID uint64
	// Caller started the call and Callee was called.
	Caller UserRef
	Callee UserRef
	// Video is true when the caller started the call with video.
	Video bool
	// State is ringing, active or ended.
	State CallState
	// CreatedAt is when the call started to ring.
	CreatedAt time.Time
	// AnsweredAt is when the callee answered; nil if never.
	AnsweredAt *time.Time
	// EndedAt is when the call ended; nil while it is open.
	EndedAt *time.Time
	// EndReason is how the call ended; empty while it is open.
	EndReason CallEndReason
	// EndedBy is the ID of the user who ended the call; 0 when it was not a
	// user (a timeout, a failure, a disabled account).
	EndedBy uint64
	// Home is the ID of the process that holds the media of the call.
	Home string
	// Incoming is true when the asking actor is the callee.
	Incoming bool
}

// Other returns the participant who is not the user with this ID.
func (c Call) Other(userID uint64) UserRef {
	if c.Caller.ID == userID {
		return c.Callee
	}
	return c.Caller
}

// CallOutcome says what became of a call, for its entry in the conversation.
type CallOutcome string

// The outcomes of a call (SPEC §7.18).
const (
	// CallCompleted is a call that was answered; it has a duration.
	CallCompleted CallOutcome = "completed"
	// CallDeclinedOutcome is a call that the callee declined.
	CallDeclinedOutcome CallOutcome = "declined"
	// CallCancelledOutcome is a call that the caller cancelled while it rang.
	CallCancelledOutcome CallOutcome = "cancelled"
	// CallMissedOutcome is a call nobody answered.
	CallMissedOutcome CallOutcome = "missed"
	// CallBusyOutcome is a call to someone who was in another call.
	CallBusyOutcome CallOutcome = "busy"
)

// CallEntry is a finished call as the conversation shows it: the content of a
// message of kind call.
type CallEntry struct {
	// CallID is the ID of the call.
	CallID uint64
	// Video is true for a call that began with video.
	Video bool
	// Outcome says what became of the call.
	Outcome CallOutcome
	// DurationSeconds is how long an answered call lasted; 0 for the others.
	DurationSeconds int
	// Text is the line the conversation shows to the asking user: "Video call
	// · 12 min", "Declined video call", "Missed video call" (the callee), "No
	// answer", "Cancelled video call" or "Video call · busy" (the caller).
	Text string
}

// outcomeOf says what became of a call that ended.
func outcomeOf(c store.Call) CallOutcome {
	switch {
	case c.AnsweredAt != nil:
		return CallCompleted
	case c.EndReason == store.CallDeclined:
		return CallDeclinedOutcome
	case c.EndReason == store.CallCancelled:
		return CallCancelledOutcome
	case c.EndReason == store.CallBusy:
		return CallBusyOutcome
	default:
		return CallMissedOutcome
	}
}

// callEntryOf makes the entry of an ended call for the user with this ID.
func callEntryOf(c store.Call, viewerID uint64) CallEntry {
	kind := "audio call"
	if c.Video {
		kind = "video call"
	}
	e := CallEntry{CallID: c.ID, Video: c.Video, Outcome: outcomeOf(c)}
	callee := viewerID == c.CalleeID
	switch e.Outcome {
	case CallCompleted:
		if c.EndedAt != nil && c.AnsweredAt != nil {
			e.DurationSeconds = max(0, int(c.EndedAt.Sub(*c.AnsweredAt)/time.Second))
		}
		e.Text = capitalized(kind) + " · " + callDuration(e.DurationSeconds)
	case CallDeclinedOutcome:
		e.Text = "Declined " + kind
	case CallCancelledOutcome:
		e.Text = "Cancelled " + kind
		if callee {
			e.Text = "Missed " + kind
		}
	case CallBusyOutcome:
		e.Text = capitalized(kind) + " · busy"
		if callee {
			e.Text = "Missed " + kind
		}
	default:
		e.Text = "No answer"
		if callee {
			e.Text = "Missed " + kind
		}
	}
	return e
}

// capitalized upper-cases the first letter of an ASCII word.
func capitalized(s string) string {
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		return s
	}
	return string(s[0]-'a'+'A') + s[1:]
}

// callDuration writes a duration for people: "45 s", "12 min", "1 h 5 min".
func callDuration(seconds int) string {
	switch {
	case seconds < 60:
		return fmt.Sprintf("%d s", seconds)
	case seconds < 3600:
		return fmt.Sprintf("%d min", seconds/60)
	case seconds%3600 < 60:
		return fmt.Sprintf("%d h", seconds/3600)
	default:
		return fmt.Sprintf("%d h %d min", seconds/3600, seconds%3600/60)
	}
}

// formField is the metadata "fields" of a refusal: the message for people
// under FieldForm.
func formField(message string) map[string]string {
	return map[string]string{FieldForm: message}
}

// Start starts a call from the actor to the other member of the direct
// conversation, with video or audio only, and rings the other member. The call
// is written in the transaction with the check that neither user is in another
// call: on PostgreSQL the rows of both users are locked first, so two calls
// started at the same time cannot both ring.
//
// A conversation that does not exist or that the actor is no member of fails
// with TagNotFound; a group conversation with TagCallGroup; calls that are off
// with TagCallsOff; a disabled callee with TagCallCalleeDisabled; a caller
// who is in a call with TagCallCallerBusy; a process that holds -max-calls
// calls with TagCallServerFull. A callee who is in another call fails with
// TagCallCalleeBusy ("Bob is in another call") after the call was written as
// ended and busy, with its entry, so that the callee has a missed call. The
// callee hears call.ringing, both users call.started.
func (c *Calls) Start(ctx context.Context, actor Actor, convID uint64, video bool) (Call, error) {
	var call store.Call
	var events []live.Event
	var busy error
	err := c.st.Tx(ctx, func(tx *store.Store) error {
		call, events, busy = store.Call{}, nil, nil
		if _, err := checkActive(ctx, tx, actor); err != nil {
			return err
		}
		conv, _, err := loadConversation(ctx, tx, actor, convID)
		if err != nil {
			return err
		}
		if !c.cfg.Enabled {
			return errors.New(TagCallsOff, "calls are off", "fields", formField(messageCallsOff))
		}
		if conv.Kind != ConversationDirect {
			return errors.New(TagCallGroup, "calls are for direct conversations", "fields", formField(messageCallGroup), "conversationID", convID)
		}
		rows, err := tx.ConversationMembers(ctx, []uint64{convID}, false)
		if err != nil {
			return err
		}
		other, ok := otherMember(rows[convID], actor.UserID)
		if !ok {
			return errors.New(TagNotFound, "the conversation has no other member",
				"reason", "member", "conversationID", convID)
		}
		if other.DisabledAt != nil {
			return errors.New(TagCallCalleeDisabled, "the callee is disabled", "fields", formField(other.DisplayName+" is disabled."), "calleeID", other.UserID)
		}
		if err := tx.LockUsersForCall(ctx, actor.UserID, other.UserID); err != nil {
			return err
		}
		open, err := tx.OpenCallsOfUsers(ctx, actor.UserID, other.UserID)
		if err != nil {
			return err
		}
		now := c.clk.Now().UTC()
		for _, o := range open {
			if o.CallerID == actor.UserID || o.CalleeID == actor.UserID {
				return errors.New(TagCallCallerBusy, "the caller is in another call", "fields", formField(messageCallCallerBusy), "callID", o.ID)
			}
		}
		if len(open) > 0 {
			// The callee is in another call: the call is written as busy, so
			// that the callee has a missed call, and the caller is told.
			call = store.Call{
				ConversationID: convID, CallerID: actor.UserID, CalleeID: other.UserID, Video: video,
				State: store.CallEnded, AnsweredAt: nil, EndedAt: &now, EndReason: store.CallBusy, Home: c.cfg.ProcessID,
			}
			if err := tx.CreateCall(ctx, &call); err != nil {
				return err
			}
			if events, err = writeCallEntry(ctx, tx, call, false); err != nil {
				return err
			}
			busy = errors.New(TagCallCalleeBusy, "the callee is in another call", "fields", formField(other.DisplayName+" is in another call."), "calleeID", other.UserID)
			return nil
		}
		if c.cfg.MaxCalls > 0 {
			n, err := tx.CountOpenCallsOfHome(ctx, c.cfg.ProcessID)
			if err != nil {
				return err
			}
			if n >= int64(c.cfg.MaxCalls) {
				return errors.New(TagCallServerFull, "the process holds as many calls as it may", "fields", formField(messageCallServerFull), "max", c.cfg.MaxCalls)
			}
		}
		call = store.Call{
			ConversationID: convID, CallerID: actor.UserID, CalleeID: other.UserID, Video: video,
			State: store.CallRinging, Home: c.cfg.ProcessID,
		}
		if err := tx.CreateCall(ctx, &call); err != nil {
			return err
		}
		events = []live.Event{
			callEvent(live.KindCallRinging, call, call.CalleeID),
			callEvent(live.KindCallStarted, call, call.CallerID),
			callEvent(live.KindCallStarted, call, call.CalleeID),
		}
		return nil
	})
	if err != nil {
		return Call{}, err
	}
	publishAll(c.pub, events)
	if busy != nil {
		return Call{}, busy
	}
	c.cfg.Logger.Info("call started", "call_id", call.ID, "conversation_id", call.ConversationID,
		"caller_id", call.CallerID, "callee_id", call.CalleeID, "video", call.Video)
	return c.view(ctx, actor, call.ID)
}

// Answer answers the ringing call as the callee. video says whether the callee
// answers with video: the pages use it to decide what to send, and the media
// server needs nothing of it, because every call has an audio and a video
// transceiver; it is not stored. Only the callee may (the caller gets
// TagForbidden); a call that does not exist or is not the actor's fails with
// TagNotFound; one that is not ringing with TagCallWrongState. Both users hear
// call.started.
func (c *Calls) Answer(ctx context.Context, actor Actor, callID uint64, video bool) (Call, error) {
	var events []live.Event
	err := c.st.Tx(ctx, func(tx *store.Store) error {
		events = nil
		call, err := c.participantCall(ctx, tx, actor, callID)
		if err != nil {
			return err
		}
		if call.CalleeID != actor.UserID {
			return errors.New(TagForbidden, "only the callee answers a call", "userID", actor.UserID, "callID", callID)
		}
		if call.State != store.CallRinging {
			return errors.New(TagCallWrongState, "the call is not ringing", "fields", formField("This call is not ringing."), "callID", callID, "state", string(call.State))
		}
		ok, err := tx.MarkCallAnswered(ctx, callID, c.clk.Now())
		if err != nil {
			return err
		}
		if !ok {
			return errors.New(TagCallWrongState, "the call is not ringing", "fields", formField("This call is not ringing."), "callID", callID)
		}
		call.State = store.CallActive
		events = []live.Event{callEvent(live.KindCallStarted, call, call.CallerID), callEvent(live.KindCallStarted, call, call.CalleeID)}
		return nil
	})
	if err != nil {
		return Call{}, err
	}
	publishAll(c.pub, events)
	c.cfg.Logger.Info("call answered", "call_id", callID, "user_id", actor.UserID, "video", video)
	return c.view(ctx, actor, callID)
}

// Decline refuses the ringing call as the callee: the call ends as declined
// and leaves its entry. The permissions and the refusals are those of Answer.
func (c *Calls) Decline(ctx context.Context, actor Actor, callID uint64) (Call, error) {
	return c.finish(ctx, actor, callID, finishRule{role: roleCallee, from: store.CallRinging, reason: store.CallDeclined})
}

// Cancel ends the ringing call as the caller: it ends as cancelled and leaves
// its entry. Only the caller may (the callee gets TagForbidden); a call that
// does not exist or is not the actor's fails with TagNotFound; one that is not
// ringing with TagCallWrongState.
func (c *Calls) Cancel(ctx context.Context, actor Actor, callID uint64) (Call, error) {
	return c.finish(ctx, actor, callID, finishRule{role: roleCaller, from: store.CallRinging, reason: store.CallCancelled})
}

// HangUp ends the active call as either side: it ends as hangup and leaves its
// entry. A call that does not exist or is not the actor's fails with
// TagNotFound; one that is not active (still ringing, or over) with
// TagCallWrongState.
func (c *Calls) HangUp(ctx context.Context, actor Actor, callID uint64) (Call, error) {
	return c.finish(ctx, actor, callID, finishRule{role: roleEither, from: store.CallActive, reason: store.CallHangup})
}

// role says which participant may make a change.
type role int

const (
	roleEither role = iota
	roleCaller
	roleCallee
)

// finishRule describes a change that ends a call.
type finishRule struct {
	// role is who may do it.
	role role
	// from is the state the call must be in.
	from store.CallState
	// reason is how the call ends.
	reason store.CallEndReason
}

// finish ends the call of callID as the actor, if the rule allows.
func (c *Calls) finish(ctx context.Context, actor Actor, callID uint64, rule finishRule) (Call, error) {
	var events []live.Event
	err := c.st.Tx(ctx, func(tx *store.Store) error {
		events = nil
		call, err := c.participantCall(ctx, tx, actor, callID)
		if err != nil {
			return err
		}
		if rule.role == roleCaller && call.CallerID != actor.UserID || rule.role == roleCallee && call.CalleeID != actor.UserID {
			return errors.New(TagForbidden, "this side may not end the call this way", "userID", actor.UserID, "callID", callID)
		}
		if call.State != rule.from {
			return errors.New(TagCallWrongState, "the call is not in the state this needs", "fields", formField("This call is not "+string(rule.from)+"."), "callID", callID, "state", string(call.State))
		}
		by := actor.UserID
		events, _, err = endCall(ctx, tx, c.clk.Now().UTC(), call.ID, rule.reason, &by)
		return err
	})
	if err != nil {
		return Call{}, err
	}
	publishAll(c.pub, events)
	c.cfg.Logger.Info("call ended", "call_id", callID, "user_id", actor.UserID, "reason", string(rule.reason))
	return c.view(ctx, actor, callID)
}

// Current returns the call in progress (ringing or active) of the direct
// conversation, and false when there is none. A conversation that does not
// exist or that the actor is no member of fails with TagNotFound, a group
// conversation with TagCallGroup.
func (c *Calls) Current(ctx context.Context, actor Actor, convID uint64) (Call, bool, error) {
	if _, err := checkActive(ctx, c.st, actor); err != nil {
		return Call{}, false, err
	}
	conv, _, err := loadConversation(ctx, c.st, actor, convID)
	if err != nil {
		return Call{}, false, err
	}
	if conv.Kind != ConversationDirect {
		return Call{}, false, errors.New(TagCallGroup, "calls are for direct conversations", "fields", formField(messageCallGroup), "conversationID", convID)
	}
	row, err := c.st.OpenCallOfConversation(ctx, convID)
	if err != nil {
		if appErr, ok := errors.FromError(err); ok && appErr.Tag == store.TagNotFound {
			return Call{}, false, nil
		}
		return Call{}, false, err
	}
	v, err := c.viewOfRow(ctx, actor, row)
	if err != nil {
		return Call{}, false, err
	}
	return v, true, nil
}

// Open is Current for the routes that act on "the call of this conversation"
// (answer, decline, cancel, hang up): it returns the call in progress, and
// fails with TagCallNone when there is none. The other failures are those of
// Current.
func (c *Calls) Open(ctx context.Context, actor Actor, convID uint64) (Call, error) {
	call, ok, err := c.Current(ctx, actor, convID)
	if err != nil {
		return Call{}, err
	}
	if !ok {
		return Call{}, errors.New(TagCallNone, "the conversation has no call in progress", "fields", formField(messageCallNone), "conversationID", convID)
	}
	return call, nil
}

// Join returns the call of the conversation that the actor may open the
// signalling socket of now: the caller while the call rings or is active, the
// callee once they have answered it. It fails with TagCallsOff when calls are
// off, with TagCallWrongState for the callee of a call that still rings, and
// as Open does.
func (c *Calls) Join(ctx context.Context, actor Actor, convID uint64) (Call, error) {
	call, err := c.Open(ctx, actor, convID)
	if err != nil {
		return Call{}, err
	}
	if !c.cfg.Enabled {
		return Call{}, errors.New(TagCallsOff, "calls are off", "fields", formField(messageCallsOff))
	}
	if call.Incoming && call.State == store.CallRinging {
		return Call{}, errors.New(TagCallWrongState, "the callee has not answered yet",
			"fields", formField("Answer the call first."), "callID", call.ID)
	}
	return call, nil
}

// Get returns the call, in any state, to one of its two participants. A call
// that does not exist or is not the actor's fails with TagNotFound.
func (c *Calls) Get(ctx context.Context, actor Actor, callID uint64) (Call, error) {
	return c.view(ctx, actor, callID)
}

// SetMedia tells the other side that the actor turned the microphone or the
// camera on or off (microphone and camera are true when on): both users hear
// call.updated. Only a participant of the active call may; a call that does
// not exist or is not the actor's fails with TagNotFound, one that is not
// active with TagCallWrongState.
func (c *Calls) SetMedia(ctx context.Context, actor Actor, callID uint64, microphone, camera bool) error {
	call, err := c.participantCall(ctx, c.st, actor, callID)
	if err != nil {
		return err
	}
	if call.State != store.CallActive {
		return errors.New(TagCallWrongState, "the call is not active", "fields", formField("This call is not active."), "callID", callID, "state", string(call.State))
	}
	for _, id := range []uint64{call.CallerID, call.CalleeID} {
		ev := callEvent(live.KindCallUpdated, call, id)
		ev.UserID, ev.Microphone, ev.Camera = actor.UserID, microphone, camera
		c.pub.Publish(ev)
	}
	return nil
}

// Expire ends every call that has rung for 45 seconds or more, by the clock of
// the service, as missed, and returns how many it ended. Each call is ended in
// a transaction of its own, so that one that fails does not hold back the
// others; the first failure is returned after all were tried. serve calls it
// every second.
func (c *Calls) Expire(ctx context.Context) (int, error) {
	rows, err := c.st.RingingCallsBefore(ctx, c.clk.Now().Add(-c.cfg.RingTimeout))
	if err != nil {
		return 0, err
	}
	var first error
	ended := 0
	for _, row := range rows {
		n, err := c.endSystem(ctx, row.ID, store.CallMissed, store.CallRinging)
		ended += n
		if err != nil && first == nil {
			first = err
		}
	}
	return ended, first
}

// Fail ends the call as failed, because its media connection did not come up
// or was lost (SPEC D64); the media server calls it. A call that has ended
// already is left alone.
func (c *Calls) Fail(ctx context.Context, callID uint64) error {
	_, err := c.endSystem(ctx, callID, store.CallFailed, "")
	return err
}

// EndOpen ends the open calls as server_stopped and returns how many it
// ended: those of the process home, or every open call when home is empty.
// serve calls it at start without Redis, when no call can be live, and at a
// graceful shutdown for its own calls.
//
// TODO(step 22.3): with Redis the start ends only the calls of dead processes.
func (c *Calls) EndOpen(ctx context.Context, home string) (int, error) {
	rows, err := c.st.OpenCalls(ctx, home)
	if err != nil {
		return 0, err
	}
	var first error
	ended := 0
	for _, row := range rows {
		n, err := c.endSystem(ctx, row.ID, store.CallServerStopped, "")
		ended += n
		if err != nil && first == nil {
			first = err
		}
	}
	return ended, first
}

// CallStatus is the state of a call as the media server asks for it: it needs
// no user.
type CallStatus struct {
	// Open is true while the call is ringing or active.
	Open bool
	// Active is true while the call is open and has been answered.
	Active bool
	// Reason is how the call ended; empty while it is open.
	Reason CallEndReason
}

// Status reads the state of a call for the media server, which asks it on its
// own behalf and not for a user: the check of who may join was made before.
// A call that does not exist is not open.
func (c *Calls) Status(ctx context.Context, callID uint64) (CallStatus, error) {
	row, err := c.st.CallByID(ctx, callID)
	if err != nil {
		if appErr, ok := errors.FromError(err); ok && appErr.Tag == store.TagNotFound {
			return CallStatus{}, nil
		}
		return CallStatus{}, err
	}
	return CallStatus{Open: row.Open(), Active: row.State == store.CallActive, Reason: row.EndReason}, nil
}

// Leave is a participant ending the call from the signalling socket: the
// caller cancels a ringing call, and either side hangs up an active one. It
// fails as Cancel and HangUp do; the callee of a ringing call has Decline.
func (c *Calls) Leave(ctx context.Context, actor Actor, callID uint64) error {
	call, err := c.participantCall(ctx, c.st, actor, callID)
	if err != nil {
		return err
	}
	if call.State == store.CallRinging && call.CallerID == actor.UserID {
		_, err = c.Cancel(ctx, actor, callID)
		return err
	}
	_, err = c.HangUp(ctx, actor, callID)
	return err
}

// IsOpen reports whether the call is ringing or active. A call that does not
// exist is not open.
func (c *Calls) IsOpen(ctx context.Context, callID uint64) (bool, error) {
	row, err := c.st.CallByID(ctx, callID)
	if err != nil {
		if appErr, ok := errors.FromError(err); ok && appErr.Tag == store.TagNotFound {
			return false, nil
		}
		return false, err
	}
	return row.Open(), nil
}

// endSystem ends the call, in a transaction of its own, because of the system
// and not of a user. only, when it is not empty, is the one state the call
// must be in: a call that is in another state is left alone. It returns 1 when
// the call was ended and 0 when it was not open or not in that state.
func (c *Calls) endSystem(ctx context.Context, callID uint64, reason store.CallEndReason, only store.CallState) (int, error) {
	var events []live.Event
	ended := 0
	err := c.st.Tx(ctx, func(tx *store.Store) error {
		events, ended = nil, 0
		row, err := tx.CallByID(ctx, callID)
		if err != nil {
			return err
		}
		if !row.Open() || only != "" && row.State != only {
			return nil
		}
		var ok bool
		events, ok, err = endCall(ctx, tx, c.clk.Now().UTC(), callID, reason, nil)
		if ok {
			ended = 1
		}
		return err
	})
	if err != nil {
		return 0, err
	}
	publishAll(c.pub, events)
	if ended > 0 {
		c.cfg.Logger.Info("call ended", "call_id", callID, "reason", string(reason))
	}
	return ended, nil
}

// participantCall loads the call for the actor: it fails with TagNotFound when
// the call does not exist or the actor is neither its caller nor its callee,
// and with TagUnauthenticated when the actor's account is disabled or gone.
func (c *Calls) participantCall(ctx context.Context, st *store.Store, actor Actor, callID uint64) (store.Call, error) {
	if _, err := checkActive(ctx, st, actor); err != nil {
		return store.Call{}, err
	}
	call, err := st.CallByID(ctx, callID)
	if err != nil {
		return store.Call{}, err
	}
	if call.CallerID != actor.UserID && call.CalleeID != actor.UserID {
		return store.Call{}, errors.New(TagNotFound, "no such call", "reason", "member", "callID", callID, "userID", actor.UserID)
	}
	return call, nil
}

// view returns the call for the actor.
func (c *Calls) view(ctx context.Context, actor Actor, callID uint64) (Call, error) {
	row, err := c.participantCall(ctx, c.st, actor, callID)
	if err != nil {
		return Call{}, err
	}
	return c.viewOfRow(ctx, actor, row)
}

// viewOfRow makes the view of a call row for the actor.
func (c *Calls) viewOfRow(ctx context.Context, actor Actor, row store.Call) (Call, error) {
	users, err := c.st.UsersByIDs(ctx, []uint64{row.CallerID, row.CalleeID})
	if err != nil {
		return Call{}, err
	}
	refs := map[uint64]UserRef{}
	for _, u := range users {
		refs[u.ID] = UserRef{ID: u.ID, Username: u.Username, DisplayName: u.DisplayName, AvatarKey: u.AvatarKey}
	}
	v := Call{
		ID:             row.ID,
		ConversationID: row.ConversationID,
		Caller:         refs[row.CallerID],
		Callee:         refs[row.CalleeID],
		Video:          row.Video,
		State:          row.State,
		CreatedAt:      row.CreatedAt,
		AnsweredAt:     row.AnsweredAt,
		EndedAt:        row.EndedAt,
		EndReason:      row.EndReason,
		Home:           row.Home,
		Incoming:       row.CalleeID == actor.UserID,
	}
	if row.EndedBy != nil {
		v.EndedBy = *row.EndedBy
	}
	return v, nil
}

// otherMember returns the member of a direct conversation who is not the user.
func otherMember(rows []store.ConversationMemberRow, userID uint64) (store.ConversationMemberRow, bool) {
	for _, r := range rows {
		if r.UserID != userID {
			return r, true
		}
	}
	return store.ConversationMemberRow{}, false
}

// callEvent is an event of the call on the topic of one user.
func callEvent(kind string, call store.Call, userID uint64) live.Event {
	return live.Event{
		Topic: live.UserTopic(userID), Kind: kind, UserID: userID,
		ConversationID: call.ConversationID, CallID: call.ID,
	}
}

// endCall ends the call in the transaction: it marks it ended with the reason
// (by is the user who ended it, nil for the system), writes its entry in the
// conversation and returns the events to publish after the commit. It returns
// false, and no events, when the call had ended already.
func endCall(ctx context.Context, tx *store.Store, now time.Time, callID uint64, reason store.CallEndReason, by *uint64) ([]live.Event, bool, error) {
	ok, err := tx.MarkCallEnded(ctx, callID, reason, by, now)
	if err != nil || !ok {
		return nil, false, err
	}
	call, err := tx.CallByID(ctx, callID)
	if err != nil {
		return nil, false, err
	}
	events, err := writeCallEntry(ctx, tx, call, true)
	if err != nil {
		return nil, false, err
	}
	return events, true, nil
}

// writeCallEntry writes the entry of an ended call in its conversation: a
// message by the caller with no body (SPEC §7.18). The caller has read it. The
// callee has read it too when they took part: they answered it, or declined it.
// A call that was missed, cancelled or busy raises the callee's unread count
// like a message. It returns the events to publish after the commit: the new
// message on the conversation, conversation.changed for both users, a read
// event for each mark that moved and, when announce is true, call.ended for
// both users.
func writeCallEntry(ctx context.Context, tx *store.Store, call store.Call, announce bool) ([]live.Event, error) {
	entry := store.Message{ConversationID: call.ConversationID, AuthorID: call.CallerID, Kind: store.MessageCall, CallID: &call.ID}
	if err := tx.CreateMessage(ctx, &entry); err != nil {
		return nil, err
	}
	var events []live.Event
	if announce {
		events = append(events, callEvent(live.KindCallEnded, call, call.CallerID), callEvent(live.KindCallEnded, call, call.CalleeID))
	}
	events = append(events, messageEvent(live.KindMessageCreated, call.ConversationID, entry.ID))
	events = append(events, changedEvents(call.ConversationID, call.CallerID, call.CalleeID)...)
	read := []uint64{call.CallerID}
	if call.AnsweredAt != nil || call.EndReason == store.CallDeclined {
		read = append(read, call.CalleeID)
	}
	for _, userID := range read {
		moved, err := tx.AdvanceLastRead(ctx, call.ConversationID, userID, entry.ID)
		if err != nil {
			return nil, err
		}
		if moved {
			events = append(events,
				live.Event{Topic: live.ConversationTopic(call.ConversationID), Kind: live.KindConversationRead,
					ConversationID: call.ConversationID, MessageID: entry.ID, UserID: userID},
				live.Event{Topic: live.UserTopic(userID), Kind: live.KindConversationRead,
					ConversationID: call.ConversationID, MessageID: entry.ID, UserID: userID})
		}
	}
	return events, nil
}

// endCallsOfUser ends the open calls of the user in the transaction, because
// their account was disabled or a session of theirs ended, and returns the
// events to publish after the commit.
func endCallsOfUser(ctx context.Context, tx *store.Store, now time.Time, userID uint64, reason store.CallEndReason) ([]live.Event, error) {
	open, err := tx.OpenCallsOfUsers(ctx, userID)
	if err != nil {
		return nil, err
	}
	var events []live.Event
	for _, call := range open {
		evs, _, err := endCall(ctx, tx, now, call.ID, reason, nil)
		if err != nil {
			return nil, err
		}
		events = append(events, evs...)
	}
	return events, nil
}
