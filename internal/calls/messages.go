package calls

import (
	"bytes"
	"encoding/json"
	"io"

	"github.com/rm4n0s/errors"
)

// The types of the messages of the signalling WebSocket (SPEC §7.18). The
// server sends offer, candidate, peer-state, ended and error; the client sends
// answer, candidate, state and hangup.
const (
	msgOffer     = "offer"
	msgAnswer    = "answer"
	msgCandidate = "candidate"
	msgState     = "state"
	msgPeerState = "peer-state"
	msgHangup    = "hangup"
	msgEnded     = "ended"
	msgError     = "error"
)

// The codes of the error messages.
const (
	// CodeInvalidMessage is the code of a message that is not a message of the
	// protocol.
	CodeInvalidMessage = "invalid_message"
	// CodeNegotiationFailed is the code of an offer, an answer or a candidate
	// that the media server could not use.
	CodeNegotiationFailed = "negotiation_failed"
	// CodeInternal is the code of a failure of the media server itself.
	CodeInternal = "internal"
	// CodeWrongState is the code of a request that the state of the call does
	// not allow.
	CodeWrongState = "wrong_state"
)

// maxSDP is the longest SDP a client may send, in bytes.
const maxSDP = 32 << 10

// serverMessage is a message of the server to the client. Only the fields of
// its type are set.
type serverMessage struct {
	Type string `json:"type"`
	// SDP is the offer.
	SDP string `json:"sdp,omitempty"`
	// Candidate, SDPMid and SDPMLineIndex are the ICE candidate.
	Candidate     string  `json:"candidate,omitempty"`
	SDPMid        *string `json:"sdpMid,omitempty"`
	SDPMLineIndex *uint16 `json:"sdpMLineIndex,omitempty"`
	// Microphone and Camera are the state of the other participant
	// (peer-state).
	Microphone *bool `json:"microphone,omitempty"`
	Camera     *bool `json:"camera,omitempty"`
	// Reason says how the call ended (ended).
	Reason string `json:"reason,omitempty"`
	// Code and Message describe a problem (error).
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

// clientMessage is a message of the client to the server.
type clientMessage struct {
	Type          string  `json:"type"`
	SDP           string  `json:"sdp"`
	Candidate     string  `json:"candidate"`
	SDPMid        *string `json:"sdpMid"`
	SDPMLineIndex *uint16 `json:"sdpMLineIndex"`
	Microphone    *bool   `json:"microphone"`
	Camera        *bool   `json:"camera"`
}

// parseClientMessage reads one message of the client. The text must be one
// JSON object with a known type, no unknown field and the fields its type
// needs. The error says what is wrong, for the error message sent back; it
// never holds the text.
func parseClientMessage(data []byte) (clientMessage, error) {
	var msg clientMessage
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&msg); err != nil {
		return clientMessage{}, errors.New(tagMessageInvalid, "the message is not a message of the protocol")
	}
	if _, err := dec.Token(); err != io.EOF {
		return clientMessage{}, errors.New(tagMessageInvalid, "the message is more than one JSON value")
	}
	switch msg.Type {
	case msgAnswer:
		if msg.SDP == "" || len(msg.SDP) > maxSDP {
			return clientMessage{}, errors.New(tagMessageInvalid, "an answer needs its sdp, at most 32 KiB")
		}
	case msgCandidate:
		if msg.Candidate == "" || len(msg.Candidate) > 2048 {
			return clientMessage{}, errors.New(tagMessageInvalid, "a candidate needs its candidate line")
		}
	case msgState:
		if msg.Microphone == nil || msg.Camera == nil {
			return clientMessage{}, errors.New(tagMessageInvalid, "a state needs microphone and camera")
		}
	case msgHangup:
	default:
		return clientMessage{}, errors.New(tagMessageInvalid, "the type of the message is not one of the protocol")
	}
	return msg, nil
}

const tagMessageInvalid = "SignallingMessageInvalid"
