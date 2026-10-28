// Package calls is the media server of the one-to-one calls (SPEC §7.18, D63,
// D64): a small WebRTC forwarding server made with pion. A call has two
// participants, each with one PeerConnection to the server, which is always
// the offerer; every RTP packet one participant sends is forwarded to the
// other. Nothing is decoded, mixed or recorded.
//
// The package is the only one that imports pion. It never imports the store:
// every permission and every change of the state of a call goes through the
// Controller, which internal/app implements, and the end of a call is learnt
// from the live events (Events). The signalling of a participant is a
// WebSocket that Server.Serve runs, after the web or the API handler has
// authenticated the user and checked that the user is a participant of the
// call.
//
// Nothing of the media, no SDP and no candidate is ever logged at info level.
package calls
