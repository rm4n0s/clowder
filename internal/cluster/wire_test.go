package cluster

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/rm4n0s/clowder/internal/live"
	"github.com/rm4n0s/errors"
)

func TestWireRoundTrip(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		ev   live.Event
	}{
		{"every field", eventN("conversation:3", 100)},
		{"only topic and kind", live.Event{Topic: "user:1", Kind: live.KindUserChanged}},
		{"largest ids", live.Event{Topic: "room:1", Kind: live.KindPostCreated, RoomID: 1<<64 - 1, MessageID: 1<<64 - 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			payload, err := json.Marshal(wireMessage{Origin: "abc", Event: wireOf(tt.ev)})
			if err != nil {
				t.Fatal(err)
			}
			origin, got, err := decodeMessage(payload)
			if err != nil {
				t.Fatalf("decodeMessage: %v", err)
			}
			if origin != "abc" || got != tt.ev {
				t.Errorf("decoded %q %+v, want abc %+v", origin, got, tt.ev)
			}
		})
	}
}

func TestWireFormat(t *testing.T) {
	t.Parallel()
	payload, err := json.Marshal(wireMessage{Origin: "o", Event: wireOf(live.Event{Topic: "room:1", Kind: "post.created", RoomID: 1, PostID: 2})})
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"origin":"o","event":{"topic":"room:1","kind":"post.created","room_id":1,"post_id":2}}`
	if string(payload) != want {
		t.Errorf("wire form = %s, want %s", payload, want)
	}
}

func TestDecodeMessageIgnoresUnknownFields(t *testing.T) {
	t.Parallel()
	_, ev, err := decodeMessage([]byte(`{"origin":"o","version":2,"event":{"topic":"t","kind":"k","extra":true}}`))
	if err != nil || ev.Topic != "t" || ev.Kind != "k" {
		t.Errorf("decodeMessage = %+v, %v", ev, err)
	}
}

func TestDecodeMessageErrors(t *testing.T) {
	t.Parallel()
	const secret = "s3cr3t-body-text"
	tests := []struct {
		name    string
		payload string
	}{
		{"not JSON", "oops " + secret},
		{"array", `["` + secret + `"]`},
		{"no origin", `{"event":{"topic":"t","kind":"k"}}`},
		{"no topic", `{"origin":"o","event":{"kind":"k"}}`},
		{"no kind", `{"origin":"o","event":{"topic":"t"}}`},
		{"empty", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, _, err := decodeMessage([]byte(tt.payload))
			appErr, ok := errors.FromError(err)
			if !ok {
				t.Fatalf("expected *errors.Error, got %T: %v", err, err)
			}
			if appErr.Tag != TagEventMalformed || !appErr.HasRoute("decodeMessage.EventMalformed") {
				t.Errorf("tag %s, route %s", appErr.Tag, appErr.Route())
			}
			if strings.Contains(appErr.Error(), secret) {
				t.Errorf("the error holds the payload: %v", appErr)
			}
		})
	}
}
