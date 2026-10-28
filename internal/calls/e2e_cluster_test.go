package calls_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/calls"
	"github.com/rm4n0s/clowder/internal/calls/callstest"
	"github.com/rm4n0s/clowder/internal/cluster"
)

// These tests are step 22.3: two processes, each a whole stack with its own
// media server and port, on one database and one real Redis (the tests are
// skipped without CLOWDER_TEST_REDIS, which scripts/test-docker.sh sets).
// Alice's page is on the first process, which holds her call; bob's page is on
// the second, which relays his signalling to the first through Redis, while
// his media goes straight to the first one's port (SPEC D65).

// redisURL returns the URL of the Redis of the tests, or skips the test.
func redisURL(t *testing.T) string {
	t.Helper()
	u := os.Getenv("CLOWDER_TEST_REDIS")
	if u == "" {
		t.Skip("CLOWDER_TEST_REDIS is not set: no Redis server for this test (scripts/test-docker.sh starts one)")
	}
	return u
}

// processID returns a new ID of a process.
func processID(t *testing.T) string {
	t.Helper()
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b[:])
}

// syncBuffer is a log destination that several goroutines write to.
type syncBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// relayLines returns the lines of the log that tell of the relay.
func (b *syncBuffer) relayLines() []string {
	var out []string
	for _, line := range strings.Split(b.String(), "\n") {
		if strings.Contains(line, "relay") {
			out = append(out, line)
		}
	}
	return out
}

// twoProcesses starts two processes of one cluster. The logs of each go to the
// buffers, at the debug level.
func twoProcesses(t *testing.T, tweak func(*stackOptions)) (a, b *stack, logA, logB *syncBuffer) {
	t.Helper()
	url := redisURL(t)
	logA, logB = &syncBuffer{}, &syncBuffer{}
	debug := func(w *syncBuffer) *slog.Logger {
		return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
	optsA := stackOptions{processID: processID(t), redisURL: url, logger: debug(logA)}
	if tweak != nil {
		tweak(&optsA)
	}
	a = newStackWith(t, optsA)
	optsB := stackOptions{processID: processID(t), redisURL: url, logger: debug(logB), first: a}
	if tweak != nil {
		tweak(&optsB)
	}
	b = newStackWith(t, optsB)
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("log of the first process:\n%s\nlog of the second process:\n%s", logA.String(), logB.String())
		}
	})
	return a, b, logA, logB
}

// startAndAnswer makes alice call bob on the first process and bob answer on
// the second: his page is there.
func startAndAnswer(t *testing.T, a, b *stack, conv uint64, video bool) uint64 {
	t.Helper()
	call, err := a.calls.Start(t.Context(), a.actor("alice"), conv, video)
	if err != nil {
		t.Fatalf("Start: %+v", err)
	}
	if call.Home != a.id {
		t.Fatalf("the home of the call is %q, want the first process %q", call.Home, a.id)
	}
	if _, err := b.calls.Answer(t.Context(), b.actor("bob"), call.ID, video); err != nil {
		t.Fatalf("Answer: %+v", err)
	}
	return call.ID
}

func TestACallAcrossTwoProcesses(t *testing.T) {
	a, b, logA, logB := twoProcesses(t, nil)
	conv := a.direct("alice", "bob")
	startAndAnswer(t, a, b, conv, true)

	alice := callstest.MustDial(t, "alice", a.pageSocket(conv), a.asPage("alice", callstest.Options{}))
	bob := callstest.MustDial(t, "bob", b.pageSocket(conv), b.asPage("bob", callstest.Options{}))
	for _, c := range []*callstest.Client{alice, bob} {
		select {
		case <-c.Connected():
		case <-time.After(20 * time.Second):
			t.Fatalf("%s did not connect", c.Name())
		}
	}
	alice.Receiving(t, 50, 20*time.Second)
	bob.Receiving(t, 50, 20*time.Second)
	t.Logf("alice (first process) received %d audio and %d video packets; bob (second process) received %d and %d",
		alice.AudioIn(), alice.VideoIn(), bob.AudioIn(), bob.VideoIn())

	// The first process holds the call; the second holds only a socket.
	if a.media.Calls() != 1 || b.media.Calls() != 0 {
		t.Errorf("calls: %d on the first process, %d on the second, want 1 and 0", a.media.Calls(), b.media.Calls())
	}
	// bob's media goes to the port of the first process: that is where the
	// candidates he was given point, and the second process did not touch them.
	homeUDP, homeTCP := a.media.Addr()
	otherUDP, otherTCP := b.media.Addr()
	candidates := strings.Join(bob.Candidates(), "\n")
	if candidates == "" {
		t.Fatal("bob got no candidates")
	}
	for _, addr := range []string{homeUDP.String(), homeTCP.String()} {
		if !strings.Contains(candidates, " "+addr[strings.LastIndex(addr, ":")+1:]+" ") {
			t.Errorf("the candidates of bob do not name the port of the first process (%s):\n%s", addr, candidates)
		}
	}
	for _, addr := range []string{otherUDP.String(), otherTCP.String()} {
		if strings.Contains(candidates, " "+addr[strings.LastIndex(addr, ":")+1:]+" ") {
			t.Errorf("the candidates of bob name the port of the second process (%s):\n%s", addr, candidates)
		}
	}

	// The microphone of bob, who is on the second process, reaches alice.
	bob.Send(map[string]any{"type": "state", "microphone": false, "camera": true})
	callstest.WaitFor(t, "alice to hear of bob's microphone", 10*time.Second, func() bool {
		states := alice.PeerStates()
		return len(states) > 0 && states[len(states)-1].Microphone != nil && !*states[len(states)-1].Microphone
	})

	// A screen share on each side needs no new offer, and the other side goes on
	// receiving.
	for _, tc := range []struct {
		name   string
		sharer *callstest.Client
		viewer *callstest.Client
	}{{"alice shares", alice, bob}, {"bob shares", bob, alice}} {
		offers := tc.sharer.Offers()
		if err := tc.sharer.ShareScreenRTP(); err != nil {
			t.Fatalf("%s: ShareScreen: %v", tc.name, err)
		}
		before := tc.viewer.VideoIn()
		callstest.WaitFor(t, tc.name+": the other side to go on receiving video", 15*time.Second, func() bool { return tc.viewer.VideoIn()-before >= 40 })
		if tc.sharer.Offers() != offers {
			t.Errorf("%s: the screen share made %d new offers", tc.name, tc.sharer.Offers()-offers)
		}
		if got := len(tc.viewer.VideoSSRCs()); got != 1 {
			t.Errorf("%s: the video arrived on %d SSRCs", tc.name, got)
		}
		t.Logf("%s: the other side received %d more video packets", tc.name, tc.viewer.VideoIn()-before)
	}

	// bob hangs up through his socket on the second process: the message goes to
	// the first, which ends the call for both, once.
	a.clk.Advance(61 * time.Second)
	bob.Send(map[string]any{"type": "hangup"})
	waitEnded(t, alice, "hangup")
	waitEnded(t, bob, "hangup")
	t.Logf("bob hung up through his socket; alice and bob were both told: ended (hangup)")
	callstest.WaitFor(t, "the sockets to close", 10*time.Second, func() bool { return alice.CloseCode() != 0 && bob.CloseCode() != 0 })
	if bob.CloseCode() != 1000 {
		t.Errorf("bob's socket closed with %d, want 1000", bob.CloseCode())
	}
	for _, user := range []string{"alice", "bob"} {
		entries := a.entries(user, conv)
		if len(entries) != 1 || entries[0].Call == nil || entries[0].Call.Outcome != "completed" || entries[0].Call.Duration != 61 {
			t.Errorf("%s: entries = %+v, want one completed call of 61 s", user, entries)
		}
	}
	callstest.WaitFor(t, "the first process to forget the call", 10*time.Second, func() bool { return a.media.Calls() == 0 })
	t.Logf("the second process (the edge: bob's socket) logged %d lines of the relay; the first (the home: the call and the media) %d", len(logB.relayLines()), len(logA.relayLines()))
	t.Logf("relay log of the second process, the edge (debug level; the types of the messages, never an SDP or a candidate):\n%s", strings.Join(logB.relayLines(), "\n"))
	t.Logf("relay log of the first process, the home:\n%s", strings.Join(logA.relayLines(), "\n"))
	for _, want := range []string{"relay: join sent", "relay: client message sent", "relay: server message received", "relay: leave sent"} {
		if !strings.Contains(logB.String(), want) {
			t.Errorf("the second process did not log %q", want)
		}
	}
	for _, want := range []string{"relay: join received", "relay: client message received", "relay: server message sent"} {
		if !strings.Contains(logA.String(), want) {
			t.Errorf("the first process did not log %q", want)
		}
	}
	// No SDP and no candidate in any relay line.
	for _, line := range append(logA.relayLines(), logB.relayLines()...) {
		if strings.Contains(line, "v=0") || strings.Contains(line, "candidate:") || strings.Contains(line, "a=") {
			t.Errorf("a relay line holds a message: %s", line)
		}
	}
}

func TestAnAPISocketOnTheOtherProcessTakesPartToo(t *testing.T) {
	a, b, _, _ := twoProcesses(t, nil)
	conv := a.direct("alice", "bob")
	startAndAnswer(t, a, b, conv, false)
	alice := callstest.MustDial(t, "alice", a.apiSocket(conv), a.asAPI("alice", callstest.Options{NoVideo: true}))
	bob := callstest.MustDial(t, "bob", b.apiSocket(conv), b.asAPI("bob", callstest.Options{NoVideo: true}))
	alice.ReceivingAudio(t, 50, 20*time.Second)
	bob.ReceivingAudio(t, 50, 20*time.Second)
	// bob's other tab, on the first process this time, replaces his socket.
	other := callstest.MustDial(t, "bob's other tab", a.pageSocket(conv), a.asPage("bob", callstest.Options{NoVideo: true}))
	callstest.WaitFor(t, "bob's first socket to be told", 10*time.Second, func() bool { return bob.CloseCode() == 4003 })
	other.ReceivingAudio(t, 20, 20*time.Second)
	alice.ReceivingAudio(t, 20, 20*time.Second)
}

func TestARelayedSocketEndsWithItsSession(t *testing.T) {
	a, b, _, _ := twoProcesses(t, func(o *stackOptions) {
		o.mediaCfg = func(c *calls.Config) { c.ReconnectGrace = 600 * time.Millisecond }
	})
	conv := a.direct("alice", "bob")
	callID := startAndAnswer(t, a, b, conv, true)
	alice := callstest.MustDial(t, "alice", a.pageSocket(conv), a.asPage("alice", callstest.Options{}))
	bob := callstest.MustDial(t, "bob", b.pageSocket(conv), b.asPage("bob", callstest.Options{}))
	alice.Receiving(t, 20, 20*time.Second)
	bob.Receiving(t, 20, 20*time.Second)

	// bob's role changes: the session of his socket is not what it was, and the
	// second process, where the socket is, closes it with the code of D44.
	if err := b.accounts.SetRole(t.Context(), b.actor("admin"), b.users["bob"].ID, "admin"); err != nil {
		t.Fatalf("SetRole: %+v", err)
	}
	callstest.WaitFor(t, "bob's socket to close with 4002", 10*time.Second, func() bool { return bob.CloseCode() == 4002 })
	if alice.CloseCode() != 0 {
		t.Errorf("alice's socket closed with %d", alice.CloseCode())
	}
	// Nobody opens a new socket for him, so his part of the call ends as it
	// does on one process (the reconnect grace of 22.1), and alice is told.
	waitEnded(t, alice, "failed")
	got, err := b.calls.Get(t.Context(), b.actor("alice"), callID)
	if err != nil || got.State != "ended" || got.EndReason != "failed" {
		t.Errorf("call = %+v, %v", got, err)
	}
}

func TestTheHomeStoppingEndsTheCallForBoth(t *testing.T) {
	tests := []struct {
		name string
		// stop is how the first process goes: Close removes its key at once, a
		// crash leaves it to expire.
		stop func(t *testing.T, a *stack)
	}{
		{"the process stops and removes its key", func(t *testing.T, a *stack) {
			if err := a.media.Close(); err != nil {
				t.Fatalf("media.Close: %+v", err)
			}
			if err := a.link.Close(); err != nil {
				t.Fatalf("link.Close: %+v", err)
			}
		}},
		{"the process dies and its key expires", func(t *testing.T, a *stack) {
			// The link goes first, so that the media server cannot tell the
			// other process anything while it closes.
			if err := a.link.Abandon(); err != nil {
				t.Fatalf("link.Abandon: %+v", err)
			}
			if err := a.media.Close(); err != nil {
				t.Fatalf("media.Close: %+v", err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, b, _, logB := twoProcesses(t, func(o *stackOptions) {
				o.link = func(c *cluster.CallLinkConfig) { c.KeyTTL, c.KeyRefresh = 600*time.Millisecond, 100*time.Millisecond }
				o.orphanGrace = 200 * time.Millisecond
			})
			conv := a.direct("alice", "bob")
			startAndAnswer(t, a, b, conv, true)
			alice := callstest.MustDial(t, "alice", a.pageSocket(conv), a.asPage("alice", callstest.Options{}))
			bob := callstest.MustDial(t, "bob", b.pageSocket(conv), b.asPage("bob", callstest.Options{}))
			alice.Receiving(t, 20, 20*time.Second)
			bob.Receiving(t, 20, 20*time.Second)

			tt.stop(t, a)

			// The second process runs the loop of serve: it ends every call whose
			// home has no live key.
			var ended atomic.Int64
			stopLoop := make(chan struct{})
			var wg sync.WaitGroup
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					select {
					case <-stopLoop:
						return
					case <-time.After(50 * time.Millisecond):
					}
					// The grace is counted on the clock of the service, which the
					// test moves with the real time.
					b.clk.Advance(50 * time.Millisecond)
					n, err := b.calls.EndOrphans(t.Context())
					if err != nil {
						t.Errorf("EndOrphans: %+v", err)
						return
					}
					ended.Add(int64(n))
				}
			}()
			defer func() {
				close(stopLoop)
				wg.Wait()
			}()

			// bob, on the second process, is told by his own process.
			waitEnded(t, bob, "server_stopped")
			callstest.WaitFor(t, "bob's socket to close", 10*time.Second, func() bool { return bob.CloseCode() == 1000 })
			// alice's socket was on the process that stopped: it was told, or it was
			// closed with it.
			callstest.WaitFor(t, "alice's socket to close", 10*time.Second, func() bool { return alice.CloseCode() != 0 })
			time.Sleep(400 * time.Millisecond)
			if got := ended.Load(); got != 1 {
				t.Errorf("the loop ended %d calls, want 1", got)
			}
			// One entry in the conversation, whatever the number of loops.
			for _, user := range []string{"alice", "bob"} {
				entries := b.entries(user, conv)
				if len(entries) != 1 {
					t.Errorf("%s: entries = %+v, want one", user, entries)
				}
			}
			cur, ok, err := b.calls.Current(t.Context(), b.actor("alice"), conv)
			if err != nil || ok {
				t.Errorf("Current = %+v, %v, %v: the call is over", cur, ok, err)
			}
			if !strings.Contains(logB.String(), "relay") {
				t.Errorf("the second process logged nothing of the relay")
			}
			t.Logf("%s: bob, on the second process, was told that the call ended (server_stopped); the loop of the second process ended %d call; relay log of the second process:\n%s",
				tt.name, ended.Load(), strings.Join(logB.relayLines(), "\n"))
		})
	}
}

func TestADeadHomeIsEndedOnceByTwoLoops(t *testing.T) {
	url := redisURL(t)
	const grace = 10 * time.Second
	a := newStackWith(t, stackOptions{processID: processID(t), redisURL: url, orphanGrace: grace})
	b := newStackWith(t, stackOptions{processID: processID(t), redisURL: url, first: a, orphanGrace: grace})
	// A process that died without a trace: it never announced itself.
	dead := newStackWith(t, stackOptions{processID: processID(t), redisURL: url, first: a, silent: true})
	convDead := a.direct("alice", "bob")
	convLive := a.direct("carol", "admin")

	ringing, err := dead.calls.Start(t.Context(), dead.actor("alice"), convDead, true)
	if err != nil {
		t.Fatalf("Start on the dead process: %+v", err)
	}
	alive, err := a.calls.Start(t.Context(), a.actor("carol"), convLive, true)
	if err != nil {
		t.Fatalf("Start on the first process: %+v", err)
	}

	// A key that is missing for less than the grace ends nothing, in either
	// process.
	for _, s := range []*stack{a, b} {
		for range 3 {
			if n, err := s.calls.EndOrphans(t.Context()); err != nil || n != 0 {
				t.Fatalf("EndOrphans before the grace = %d, %v", n, err)
			}
			a.clk.Advance(grace / 4)
		}
	}
	a.clk.Advance(grace)

	// Past the grace both live processes run the loop at the same time, many times.
	var total atomic.Int64
	var wg sync.WaitGroup
	for _, s := range []*stack{a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 40 {
				n, err := s.calls.EndOrphans(t.Context())
				if err != nil {
					t.Errorf("EndOrphans: %+v", err)
					return
				}
				total.Add(int64(n))
				time.Sleep(5 * time.Millisecond)
			}
		}()
	}
	wg.Wait()
	if got := total.Load(); got != 1 {
		t.Errorf("the loops ended %d calls, want 1", got)
	}
	got, err := a.calls.Get(t.Context(), a.actor("alice"), ringing.ID)
	if err != nil || got.State != "ended" || got.EndReason != "server_stopped" {
		t.Errorf("the call of the dead process = %+v, %v", got, err)
	}
	for _, user := range []string{"alice", "bob"} {
		if entries := a.entries(user, convDead); len(entries) != 1 {
			t.Errorf("%s: entries = %+v, want exactly one", user, entries)
		}
	}
	// The call of a live process is left alone.
	other, err := a.calls.Get(t.Context(), a.actor("carol"), alive.ID)
	if err != nil || other.State == "ended" {
		t.Errorf("the call of the live process = %+v, %v", other, err)
	}
}

func TestTheHomeIsTheProcessThatStartedTheCall(t *testing.T) {
	// The ID of the process that handled Start is the home, whatever process
	// reads the call afterwards.
	a, b, _, _ := twoProcesses(t, nil)
	conv := a.direct("alice", "bob")
	call, err := a.calls.Start(t.Context(), a.actor("alice"), conv, false)
	if err != nil {
		t.Fatalf("Start: %+v", err)
	}
	for _, s := range []*stack{a, b} {
		got, err := s.calls.Get(t.Context(), s.actor("bob"), call.ID)
		if err != nil || got.Home != a.id {
			t.Errorf("read on %s: home = %q, %v; want %q", strconv.Quote(s.id), got.Home, err, a.id)
		}
	}
	// The page socket of the second process for a call that rings is refused
	// like the first one's: bob has not answered.
	if _, resp, err := callstest.Dial(t, "bob early", b.pageSocket(conv), b.asPage("bob", callstest.Options{})); err == nil || resp == nil || resp.StatusCode != http.StatusConflict {
		t.Errorf("a socket of a callee who has not answered: err=%v resp=%v", err, resp)
	}
}

// TestRedisLosingItsKeysEndsNoCall: Redis restarts and loses every key, so for a
// while no process looks alive; the processes write their keys again at the
// next refresh, and the loop of the grace period has ended nothing (SPEC D65).
// Past the grace a key that stays missing does end the call, once.
func TestRedisLosingItsKeysEndsNoCall(t *testing.T) {
	url := redisURL(t)
	const grace = 20 * time.Second
	var lost atomic.Bool
	forgetful := func(real app.ProcessDirectory) app.ProcessDirectory {
		return directoryFunc(func(ctx context.Context, id string) (bool, error) {
			if lost.Load() {
				return false, nil
			}
			return real.Alive(ctx, id)
		})
	}
	a := newStackWith(t, stackOptions{processID: processID(t), redisURL: url, orphanGrace: grace, directory: forgetful})
	b := newStackWith(t, stackOptions{processID: processID(t), redisURL: url, first: a, orphanGrace: grace, directory: forgetful})
	conv := a.direct("alice", "bob")
	callID := startAndAnswer(t, a, b, conv, true)
	alice := callstest.MustDial(t, "alice", a.pageSocket(conv), a.asPage("alice", callstest.Options{}))
	bob := callstest.MustDial(t, "bob", b.pageSocket(conv), b.asPage("bob", callstest.Options{}))
	alice.Receiving(t, 20, 20*time.Second)
	bob.Receiving(t, 20, 20*time.Second)

	rounds := func(n int, step time.Duration) int {
		total := 0
		for range n {
			a.clk.Advance(step)
			for _, s := range []*stack{a, b} {
				got, err := s.calls.EndOrphans(t.Context())
				if err != nil {
					t.Fatalf("EndOrphans: %+v", err)
				}
				total += got
			}
		}
		return total
	}
	// Redis lost the keys: for 15 s, less than the grace, no process looks
	// alive. Then the processes refresh their keys.
	lost.Store(true)
	if n := rounds(15, time.Second); n != 0 {
		t.Fatalf("the loops ended %d calls within the grace", n)
	}
	lost.Store(false)
	if n := rounds(30, time.Second); n != 0 {
		t.Fatalf("the loops ended %d calls after the keys came back", n)
	}
	alice.Receiving(t, 20, 20*time.Second)
	bob.Receiving(t, 20, 20*time.Second)
	if cur, ok, err := a.calls.Current(t.Context(), a.actor("alice"), conv); err != nil || !ok || cur.ID != callID || cur.State != "active" {
		t.Fatalf("the call = %+v, %v, %v; want it still active", cur, ok, err)
	}
	// A key that stays missing past the grace ends the call, once.
	lost.Store(true)
	if n := rounds(25, time.Second); n != 1 {
		t.Fatalf("the loops ended %d calls past the grace, want 1", n)
	}
	waitEnded(t, alice, "server_stopped")
	waitEnded(t, bob, "server_stopped")
	if entries := a.entries("alice", conv); len(entries) != 1 {
		t.Errorf("entries = %+v, want one", entries)
	}
}

// directoryFunc is a ProcessDirectory made of a function.
type directoryFunc func(ctx context.Context, process string) (bool, error)

func (f directoryFunc) Alive(ctx context.Context, process string) (bool, error) {
	return f(ctx, process)
}
