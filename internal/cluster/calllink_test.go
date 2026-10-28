package cluster

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/rm4n0s/clowder/internal/calls"
	"github.com/rm4n0s/errors"
)

// newTestLink makes a link over the fake Redis of broker. The test closes it.
func newTestLink(t *testing.T, broker *linkBroker, id string, cfg CallLinkConfig) (*CallLink, *fakeLinkBackend, *syncBuffer) {
	t.Helper()
	backend := newFakeLinkBackend(broker)
	logs := &syncBuffer{}
	cfg.ProcessID = id
	cfg.Logger = slog.New(slog.NewJSONHandler(logs, nil))
	l, err := newCallLink(t.Context(), backend, cfg)
	if err != nil {
		t.Fatalf("newCallLink: %+v", err)
	}
	t.Cleanup(func() {
		if err := l.Close(); err != nil {
			t.Errorf("Close: %+v", err)
		}
	})
	return l, backend, logs
}

// nextEnvelope waits for the next envelope of the link.
func nextEnvelope(t *testing.T, l *CallLink) calls.Envelope {
	t.Helper()
	select {
	case env, ok := <-l.Messages():
		if !ok {
			t.Fatal("Messages was closed")
		}
		return env
	case <-time.After(5 * time.Second):
		t.Fatal("no envelope arrived")
	}
	return calls.Envelope{}
}

func TestCallLinkDeliversEnvelopesToTheirProcessOnly(t *testing.T) {
	t.Parallel()
	broker := newLinkBroker()
	a, _, _ := newTestLink(t, broker, "aaaa", CallLinkConfig{})
	b, _, _ := newTestLink(t, broker, "bbbb", CallLinkConfig{})
	c, _, _ := newTestLink(t, broker, "cccc", CallLinkConfig{})

	want := calls.Envelope{Kind: calls.EnvClient, From: "aaaa", CallID: 7, UserID: 20, Socket: "s1", Message: json.RawMessage(`{"type":"hangup"}`)}
	if err := a.Send(t.Context(), "bbbb", want); err != nil {
		t.Fatalf("Send: %+v", err)
	}
	got := nextEnvelope(t, b)
	if got.Kind != want.Kind || got.From != want.From || got.CallID != 7 || got.UserID != 20 || got.Socket != "s1" || string(got.Message) != `{"type":"hangup"}` {
		t.Errorf("got %+v, want %+v", got, want)
	}
	// And the answer goes the other way.
	back := calls.Envelope{Kind: calls.EnvClose, From: "bbbb", CallID: 7, UserID: 20, Socket: "s1", Code: 1000, Reason: "call ended"}
	if err := b.Send(t.Context(), "aaaa", back); err != nil {
		t.Fatalf("Send: %+v", err)
	}
	if got := nextEnvelope(t, a); got.Kind != calls.EnvClose || got.Code != 1000 || got.Reason != "call ended" {
		t.Errorf("got %+v", got)
	}
	select {
	case env := <-c.Messages():
		t.Errorf("a third process got %+v", env)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestCallLinkKeepsTheOrderOfTheEnvelopes(t *testing.T) {
	t.Parallel()
	broker := newLinkBroker()
	a, _, _ := newTestLink(t, broker, "aaaa", CallLinkConfig{})
	b, _, _ := newTestLink(t, broker, "bbbb", CallLinkConfig{})
	const n = 300
	for i := range n {
		env := calls.Envelope{Kind: calls.EnvLeave, From: "aaaa", CallID: uint64(i + 1), UserID: 1, Socket: "s"}
		if err := a.Send(t.Context(), "bbbb", env); err != nil {
			t.Fatalf("Send: %+v", err)
		}
	}
	for i := range n {
		if got := nextEnvelope(t, b); got.CallID != uint64(i+1) {
			t.Fatalf("envelope %d has the call %d", i, got.CallID)
		}
	}
}

func TestCallLinkSendFailures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		to    string
		setup func(*fakeLinkBackend)
		env   calls.Envelope
		tag   string
		route string
	}{
		{"nobody listens there", "dead", nil, calls.Envelope{Kind: calls.EnvJoin, From: "aaaa", CallID: 1, Socket: "s"},
			TagCallNobodyListening, "CallLink.Send.CallNobodyListening"},
		{"Redis fails", "bbbb", func(f *fakeLinkBackend) { f.publishErr = errFakeRedis }, calls.Envelope{Kind: calls.EnvLeave, From: "aaaa", CallID: 1, Socket: "s"},
			"FakeRedisDown", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			broker := newLinkBroker()
			a, backend, _ := newTestLink(t, broker, "aaaa", CallLinkConfig{})
			newTestLink(t, broker, "bbbb", CallLinkConfig{})
			if tt.setup != nil {
				tt.setup(backend)
			}
			err := a.Send(t.Context(), tt.to, tt.env)
			appErr, ok := errors.FromError(err)
			if !ok {
				t.Fatalf("expected *errors.Error, got %T: %v", err, err)
			}
			if appErr.Tag != tt.tag || (tt.route != "" && !appErr.HasRoute(tt.route)) {
				t.Errorf("tag %s route %s", appErr.Tag, appErr.Route())
			}
			if tt.tag == TagCallNobodyListening && appErr.Metadata["to"] != "dead" {
				t.Errorf("metadata = %v", appErr.Metadata)
			}
		})
	}

	t.Run("a message that cannot be encoded", func(t *testing.T) {
		t.Parallel()
		a, _, _ := newTestLink(t, newLinkBroker(), "aaaa", CallLinkConfig{})
		// Raw JSON that is not JSON makes the marshaling fail.
		err := a.Send(t.Context(), "bbbb", calls.Envelope{Kind: calls.EnvClient, Message: json.RawMessage(`{`)})
		appErr, ok := errors.FromError(err)
		if !ok || appErr.Tag != TagCallEnvelopeEncodeFailed || !appErr.HasRoute("CallLink.Send.CallEnvelopeEncodeFailed") || appErr.Metadata["kind"] != calls.EnvClient {
			t.Errorf("err = %v", err)
		}
	})
}

func TestDecodeEnvelope(t *testing.T) {
	t.Parallel()
	valid := map[string]string{
		"a join":    `{"kind":"join","from":"a","call_id":1,"user_id":2,"socket":"s","join":{"conversation_id":3,"caller_id":2,"callee_id":4}}`,
		"a client":  `{"kind":"client","from":"a","call_id":1,"user_id":2,"socket":"s","message":{"type":"hangup"}}`,
		"a server":  `{"kind":"server","from":"a","call_id":1,"user_id":2,"socket":"s","message":{"type":"ended"}}`,
		"a leave":   `{"kind":"leave","from":"a","call_id":1,"user_id":2,"socket":"s"}`,
		"a close":   `{"kind":"close","from":"a","call_id":1,"user_id":2,"socket":"s","code":4003,"reason":"replaced"}`,
		"extra key": `{"kind":"leave","from":"a","call_id":1,"user_id":2,"socket":"s","later":true}`,
	}
	for name, payload := range valid {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			env, err := decodeEnvelope([]byte(payload))
			if err != nil || env.CallID != 1 || env.From != "a" || env.Socket != "s" {
				t.Errorf("env = %+v, err = %v", env, err)
			}
		})
	}
	invalid := map[string]string{
		"not JSON":               `{`,
		"not an object":          `[1]`,
		"an unknown kind":        `{"kind":"hello","from":"a","call_id":1,"socket":"s"}`,
		"no kind":                `{"from":"a","call_id":1,"socket":"s"}`,
		"a join without a call":  `{"kind":"join","from":"a","call_id":1,"socket":"s"}`,
		"a client message empty": `{"kind":"client","from":"a","call_id":1,"socket":"s"}`,
		"no sender":              `{"kind":"leave","call_id":1,"socket":"s"}`,
		"no call":                `{"kind":"leave","from":"a","socket":"s"}`,
		"no socket":              `{"kind":"leave","from":"a","call_id":1}`,
	}
	for name, payload := range invalid {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := decodeEnvelope([]byte(payload))
			appErr, ok := errors.FromError(err)
			if !ok || appErr.Tag != TagCallEnvelopeMalformed || !appErr.HasRoute("decodeEnvelope.CallEnvelopeMalformed") {
				t.Fatalf("err = %v", err)
			}
			if strings.Contains(appErr.Error(), "hello") || strings.Contains(strings.Join(metadataStrings(appErr), " "), "call_id") {
				t.Errorf("the error holds the payload: %v %v", appErr, appErr.Metadata)
			}
		})
	}
}

// metadataStrings returns the metadata values that are strings.
func metadataStrings(e *errors.Error) []string {
	var out []string
	for _, v := range e.Metadata {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func TestCallLinkIgnoresMalformedMessages(t *testing.T) {
	t.Parallel()
	broker := newLinkBroker()
	a, backend, logs := newTestLink(t, broker, "aaaa", CallLinkConfig{})
	b, _, _ := newTestLink(t, broker, "bbbb", CallLinkConfig{})
	backend.subs[0].inject(`not json at all`)
	backend.subs[0].inject(`{"kind":"leave"}`)
	good := calls.Envelope{Kind: calls.EnvLeave, From: "bbbb", CallID: 5, UserID: 1, Socket: "s"}
	if err := b.Send(t.Context(), "aaaa", good); err != nil {
		t.Fatalf("Send: %+v", err)
	}
	if got := nextEnvelope(t, a); got.CallID != 5 {
		t.Errorf("got %+v", got)
	}
	if len(logs.entries(t, "malformed call message")) == 0 {
		t.Errorf("the malformed message was not logged:\n%s", logs.String())
	}
}

func TestCallLinkAnnounce(t *testing.T) {
	t.Parallel()
	broker := newLinkBroker()
	cfg := CallLinkConfig{KeyTTL: 120 * time.Millisecond, KeyRefresh: 20 * time.Millisecond}
	a, backend, _ := newTestLink(t, broker, "aaaa", cfg)
	other, _, _ := newTestLink(t, broker, "bbbb", cfg)

	if alive, err := other.Alive(t.Context(), "aaaa"); err != nil || alive {
		t.Fatalf("Alive before the announcement = %v, %v", alive, err)
	}
	if err := a.Announce(t.Context(), "10.0.0.2:7882"); err != nil {
		t.Fatalf("Announce: %+v", err)
	}
	if v, ok := broker.value(ProcessKeyPrefix + "aaaa"); !ok || v != "10.0.0.2:7882" {
		t.Errorf("the key holds %q (%v), want the media address", v, ok)
	}
	if alive, err := other.Alive(t.Context(), "aaaa"); err != nil || !alive {
		t.Errorf("Alive after the announcement = %v, %v", alive, err)
	}
	// A second announcement changes nothing.
	sets := backend.setCount()
	if err := a.Announce(t.Context(), "other:1"); err != nil {
		t.Fatalf("second Announce: %+v", err)
	}
	if v, _ := broker.value(ProcessKeyPrefix + "aaaa"); v != "10.0.0.2:7882" {
		t.Errorf("the second announcement changed the value to %q", v)
	}
	_ = sets

	// The refresher keeps the key alive well past its time to live.
	time.Sleep(400 * time.Millisecond)
	if alive, _ := other.Alive(t.Context(), "aaaa"); !alive {
		t.Error("the key expired although the process refreshes it")
	}
	if backend.setCount() < 5 {
		t.Errorf("the key was written %d times in 400 ms", backend.setCount())
	}
}

func TestCallLinkCloseRemovesTheKeyAndStopsTheGoroutines(t *testing.T) {
	t.Parallel()
	broker := newLinkBroker()
	cfg := CallLinkConfig{KeyTTL: 10 * time.Second, KeyRefresh: 10 * time.Millisecond}
	a, backend, _ := newTestLink(t, broker, "aaaa", cfg)
	if err := a.Announce(t.Context(), "10.0.0.2:7882"); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatalf("Close: %+v", err)
	}
	if _, ok := broker.value(ProcessKeyPrefix + "aaaa"); ok {
		t.Error("the key is still there after Close")
	}
	if !backend.isClosed() {
		t.Error("the connection was not closed")
	}
	// The messages channel is closed, and the refresher does not write the key
	// back.
	if _, ok := <-a.Messages(); ok {
		t.Error("Messages is still open after Close")
	}
	sets := backend.setCount()
	time.Sleep(60 * time.Millisecond)
	if backend.setCount() != sets {
		t.Error("the key was written after Close")
	}
	if _, ok := broker.value(ProcessKeyPrefix + "aaaa"); ok {
		t.Error("the key came back after Close")
	}
	// Closing again is fine and gives the same result.
	if err := a.Close(); err != nil {
		t.Errorf("the second Close: %+v", err)
	}
}

func TestCallLinkAbandonLeavesTheKeyToExpire(t *testing.T) {
	t.Parallel()
	broker := newLinkBroker()
	cfg := CallLinkConfig{KeyTTL: 150 * time.Millisecond, KeyRefresh: 20 * time.Millisecond}
	a, _, _ := newTestLink(t, broker, "aaaa", cfg)
	other, _, _ := newTestLink(t, broker, "bbbb", cfg)
	if err := a.Announce(t.Context(), "10.0.0.2:7882"); err != nil {
		t.Fatal(err)
	}
	if err := a.Abandon(); err != nil {
		t.Fatalf("Abandon: %+v", err)
	}
	if alive, _ := other.Alive(t.Context(), "aaaa"); !alive {
		t.Error("the key was removed at once: Abandon must leave it to expire")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		alive, err := other.Alive(t.Context(), "aaaa")
		if err != nil {
			t.Fatal(err)
		}
		if !alive {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the key of the abandoned process did not expire")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestCallLinkFailures(t *testing.T) {
	t.Parallel()
	t.Run("a failure to announce is returned and starts no refresher", func(t *testing.T) {
		t.Parallel()
		broker := newLinkBroker()
		a, backend, _ := newTestLink(t, broker, "aaaa", CallLinkConfig{KeyRefresh: 10 * time.Millisecond})
		backend.setErr = errFakeRedis
		err := a.Announce(t.Context(), "x:1")
		if appErr, ok := errors.FromError(err); !ok || appErr.Tag != "FakeRedisDown" {
			t.Fatalf("err = %v", err)
		}
		backend.mu.Lock()
		backend.setErr = nil
		backend.mu.Unlock()
		time.Sleep(60 * time.Millisecond)
		if _, ok := broker.value(ProcessKeyPrefix + "aaaa"); ok {
			t.Error("a refresher is running although Announce failed")
		}
		// And a later Announce works.
		if err := a.Announce(t.Context(), "x:1"); err != nil {
			t.Errorf("Announce: %+v", err)
		}
	})
	t.Run("a failed refresh is logged and tried again", func(t *testing.T) {
		t.Parallel()
		broker := newLinkBroker()
		a, backend, logs := newTestLink(t, broker, "aaaa", CallLinkConfig{KeyTTL: 10 * time.Second, KeyRefresh: 10 * time.Millisecond})
		if err := a.Announce(t.Context(), "x:1"); err != nil {
			t.Fatal(err)
		}
		backend.mu.Lock()
		backend.setErr = errFakeRedis
		backend.mu.Unlock()
		deadline := time.Now().Add(5 * time.Second)
		for !strings.Contains(logs.String(), "refreshing the key of this process") && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if !strings.Contains(logs.String(), "refreshing the key of this process") {
			t.Fatalf("not logged:\n%s", logs.String())
		}
		backend.mu.Lock()
		backend.setErr = nil
		backend.mu.Unlock()
		sets := backend.setCount()
		deadline = time.Now().Add(5 * time.Second)
		for backend.setCount() < sets+3 && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if backend.setCount() < sets+3 {
			t.Error("the refresher gave up after a failure")
		}
	})
	t.Run("Alive returns the error of Redis", func(t *testing.T) {
		t.Parallel()
		a, backend, _ := newTestLink(t, newLinkBroker(), "aaaa", CallLinkConfig{})
		backend.existsErr = errFakeRedis
		alive, err := a.Alive(t.Context(), "bbbb")
		if appErr, ok := errors.FromError(err); alive || !ok || appErr.Tag != "FakeRedisDown" {
			t.Errorf("Alive = %v, %v", alive, err)
		}
	})
	t.Run("a failure to subscribe closes the backend", func(t *testing.T) {
		t.Parallel()
		backend := newFakeLinkBackend(newLinkBroker())
		backend.subscribeErr = errFakeRedis
		l, err := newCallLink(t.Context(), backend, CallLinkConfig{ProcessID: "aaaa"})
		if l != nil || err == nil || !backend.isClosed() {
			t.Errorf("link = %v, err = %v, closed = %v", l, err, backend.isClosed())
		}
	})
	t.Run("a failure to remove the key at Close is logged", func(t *testing.T) {
		t.Parallel()
		backend := newFakeLinkBackend(newLinkBroker())
		logs := &syncBuffer{}
		l, err := newCallLink(t.Context(), backend, CallLinkConfig{ProcessID: "aaaa", Logger: slog.New(slog.NewJSONHandler(logs, nil))})
		if err != nil {
			t.Fatal(err)
		}
		if err := l.Announce(t.Context(), "x:1"); err != nil {
			t.Fatal(err)
		}
		backend.deleteErr = errFakeRedis
		backend.closeErr = errors.New("FakeCloseFailed", "no")
		err = l.Close()
		if appErr, ok := errors.FromError(err); !ok || appErr.Tag != "FakeCloseFailed" {
			t.Errorf("Close = %v", err)
		}
		if !strings.Contains(logs.String(), "removing the key of this process from Redis failed") {
			t.Errorf("not logged:\n%s", logs.String())
		}
	})
}

func TestRedisProcessCommandsFailWithTheirTag(t *testing.T) {
	t.Parallel()
	// A client for a port where nothing listens: every command fails.
	c := &redisClient{rc: redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", MaxRetries: -1, DialTimeout: 200 * time.Millisecond})}
	t.Cleanup(func() {
		if err := c.rc.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	ctx := t.Context()
	tests := []struct {
		name  string
		run   func() error
		tag   string
		route string
		op    string
	}{
		{"setKey", func() error { return c.setKey(ctx, "k", "v", time.Second) }, TagRedisProcessFailed, "redisClient.setKey.RedisProcessFailed", "set"},
		{"keyExists", func() error { _, err := c.keyExists(ctx, "k"); return err }, TagRedisProcessFailed, "redisClient.keyExists.RedisProcessFailed", "exists"},
		{"deleteKey", func() error { return c.deleteKey(ctx, "k") }, TagRedisProcessFailed, "redisClient.deleteKey.RedisProcessFailed", "delete"},
		{"publishCount", func() error { _, err := c.publishCount(ctx, "ch", []byte("x")); return err }, TagRedisPublishFailed, "redisClient.publishCount.RedisPublishFailed", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.run()
			appErr, ok := errors.FromError(err)
			if !ok {
				t.Fatalf("expected *errors.Error, got %T: %v", err, err)
			}
			if appErr.Tag != tt.tag || !appErr.HasRoute(tt.route) {
				t.Errorf("tag %s route %s", appErr.Tag, appErr.Route())
			}
			if tt.op != "" && appErr.Metadata["op"] != tt.op {
				t.Errorf("metadata = %v", appErr.Metadata)
			}
		})
	}
}

// ---- the same on a real Redis (skipped without CLOWDER_TEST_REDIS) ----

// newRealLink opens a link on the real Redis with a unique process ID.
func newRealLink(t *testing.T, url, id string, cfg CallLinkConfig) *CallLink {
	t.Helper()
	cfg.RedisURL, cfg.ProcessID = url, id
	l, err := NewCallLink(t.Context(), cfg)
	if err != nil {
		t.Fatalf("NewCallLink: %+v", err)
	}
	t.Cleanup(func() {
		if err := l.Close(); err != nil {
			t.Errorf("Close: %+v", err)
		}
	})
	return l
}

func TestRealRedisCallLinkRelaysAndAnnounces(t *testing.T) {
	t.Parallel()
	url := testRedisURL(t)
	idA, idB := "link-test-a-"+t.Name(), "link-test-b-"+t.Name()
	cfg := CallLinkConfig{KeyTTL: 300 * time.Millisecond, KeyRefresh: 50 * time.Millisecond}
	a, b := newRealLink(t, url, idA, cfg), newRealLink(t, url, idB, cfg)

	for i := range 50 {
		env := calls.Envelope{Kind: calls.EnvClient, From: idA, CallID: uint64(i + 1), UserID: 3, Socket: "s", Message: json.RawMessage(`{"type":"hangup"}`)}
		if err := a.Send(t.Context(), idB, env); err != nil {
			t.Fatalf("Send: %+v", err)
		}
	}
	for i := range 50 {
		if got := nextEnvelope(t, b); got.CallID != uint64(i+1) || got.From != idA || string(got.Message) != `{"type":"hangup"}` {
			t.Fatalf("envelope %d = %+v", i, got)
		}
	}
	err := a.Send(t.Context(), "link-test-nobody-"+t.Name(), calls.Envelope{Kind: calls.EnvLeave, From: idA, CallID: 1, Socket: "s"})
	if appErr, ok := errors.FromError(err); !ok || appErr.Tag != TagCallNobodyListening {
		t.Errorf("Send to nobody = %v", err)
	}

	// The key: made at once, refreshed past its time to live, gone at Close.
	if alive, err := b.Alive(t.Context(), idA); err != nil || alive {
		t.Fatalf("Alive before Announce = %v, %v", alive, err)
	}
	if err := a.Announce(t.Context(), "10.0.0.2:7882"); err != nil {
		t.Fatalf("Announce: %+v", err)
	}
	if alive, err := b.Alive(t.Context(), idA); err != nil || !alive {
		t.Fatalf("Alive after Announce = %v, %v", alive, err)
	}
	time.Sleep(900 * time.Millisecond)
	if alive, _ := b.Alive(t.Context(), idA); !alive {
		t.Error("the key expired although it is refreshed")
	}
	if err := a.Close(); err != nil {
		t.Fatalf("Close: %+v", err)
	}
	if alive, _ := b.Alive(t.Context(), idA); alive {
		t.Error("the key is still alive after Close")
	}
}

func TestRealRedisAbandonedProcessExpires(t *testing.T) {
	t.Parallel()
	url := testRedisURL(t)
	idA, idB := "link-test-dead-"+t.Name(), "link-test-watch-"+t.Name()
	cfg := CallLinkConfig{KeyTTL: 400 * time.Millisecond, KeyRefresh: 60 * time.Millisecond}
	a, b := newRealLink(t, url, idA, cfg), newRealLink(t, url, idB, cfg)
	if err := a.Announce(t.Context(), "10.0.0.2:7882"); err != nil {
		t.Fatal(err)
	}
	if err := a.Abandon(); err != nil {
		t.Fatalf("Abandon: %+v", err)
	}
	if alive, _ := b.Alive(t.Context(), idA); !alive {
		t.Error("the key was removed at once")
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		alive, err := b.Alive(t.Context(), idA)
		if err != nil {
			t.Fatal(err)
		}
		if !alive {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the key of the abandoned process did not expire")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

var _ context.Context
