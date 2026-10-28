package calls

import (
	"go/parser"
	"go/token"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/rm4n0s/clowder/internal/calls/callstest"
	"github.com/rm4n0s/clowder/internal/live"
	"github.com/rm4n0s/errors"
)

func TestParseClientMessage(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantErr bool
		want    string // the type
	}{
		{"an answer", `{"type":"answer","sdp":"v=0"}`, false, msgAnswer},
		{"an answer without sdp", `{"type":"answer"}`, true, ""},
		{"an answer with a long sdp", `{"type":"answer","sdp":"` + strings.Repeat("a", maxSDP+1) + `"}`, true, ""},
		{"a candidate", `{"type":"candidate","candidate":"candidate:1 1 udp 1 1.2.3.4 5 typ host","sdpMid":"0","sdpMLineIndex":0}`, false, msgCandidate},
		{"a candidate without its line", `{"type":"candidate","sdpMid":"0"}`, true, ""},
		{"a state", `{"type":"state","microphone":true,"camera":false}`, false, msgState},
		{"a state without the camera", `{"type":"state","microphone":true}`, true, ""},
		{"a hangup", `{"type":"hangup"}`, false, msgHangup},
		{"an unknown type", `{"type":"offer","sdp":"v=0"}`, true, ""},
		{"no type", `{}`, true, ""},
		{"an unknown field", `{"type":"hangup","reason":"x"}`, true, ""},
		{"not an object", `[1,2]`, true, ""},
		{"not JSON", `hello`, true, ""},
		{"two values", `{"type":"hangup"}{"type":"hangup"}`, true, ""},
		{"empty", ``, true, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg, err := parseClientMessage([]byte(tt.in))
			if tt.wantErr {
				appErr, ok := errors.FromError(err)
				if !ok {
					t.Fatalf("got %v, want a tagged error", err)
				}
				if appErr.Tag != tagMessageInvalid || !appErr.HasRoute("parseClientMessage.SignallingMessageInvalid") {
					t.Errorf("tag %s route %s", appErr.Tag, appErr.Route())
				}
				if strings.Contains(appErr.Message, "hello") {
					t.Errorf("the error repeats the text: %q", appErr.Message)
				}
				return
			}
			if err != nil || msg.Type != tt.want {
				t.Errorf("got %+v, %v; want type %s", msg, err, tt.want)
			}
		})
	}
}

func TestLimitVideoBandwidth(t *testing.T) {
	const offer = "v=0\r\no=- 1 1 IN IP4 0.0.0.0\r\ns=-\r\nt=0 0\r\n" +
		"m=audio 9 UDP/TLS/RTP/SAVPF 111\r\nc=IN IP4 0.0.0.0\r\nb=AS:64\r\na=mid:0\r\na=rtpmap:111 opus/48000/2\r\n" +
		"m=video 9 UDP/TLS/RTP/SAVPF 96\r\nc=IN IP4 0.0.0.0\r\nb=AS:9999\r\nb=CT:1\r\na=mid:1\r\na=rtpmap:96 VP8/90000\r\n"
	got, err := limitVideoBandwidth(offer, 2_500_000)
	if err != nil {
		t.Fatalf("%+v", err)
	}
	audio, video, _ := strings.Cut(got, "m=video")
	if !strings.Contains(audio, "b=AS:64\r\n") {
		t.Errorf("the audio section changed:\n%s", audio)
	}
	if strings.Count(video, "b=AS:") != 1 || !strings.Contains(video, "b=AS:2500\r\n") || !strings.Contains(video, "b=TIAS:2500000\r\n") ||
		!strings.Contains(video, "b=CT:1\r\n") || strings.Contains(video, "9999") {
		t.Errorf("the video section = %q", video)
	}
	if _, err := limitVideoBandwidth("not an sdp", 1000); err == nil {
		t.Error("an SDP that is not one was accepted")
	} else {
		appErr, ok := errors.FromError(err)
		if !ok || appErr.Tag != tagSDPInvalid || !appErr.HasRoute("limitVideoBandwidth.SDPInvalid") {
			t.Errorf("error = %v", err)
		}
	}
}

func TestNewFailures(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = busy.Close() })
	tests := []struct {
		name  string
		addr  string
		route string
		step  string
	}{
		{"an address that is not host:port", "7882", "New->listenMedia.MediaListenFailed", "parse"},
		{"a port that is not a number", "127.0.0.1:http-alt", "New->listenMedia.MediaListenFailed", "parse"},
		{"a port that is out of range", "127.0.0.1:70000", "New->listenMedia.MediaListenFailed", "parse"},
		{"a TCP port in use", busy.Addr().String(), "New->listenMedia.MediaListenFailed", "tcp"},
		{"a host that is no address of the machine", "192.0.2.1:0", "New->listenMedia.MediaListenFailed", "tcp"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hub := live.New()
			defer hub.Close()
			_, err := New(t.Context(), Config{Addr: tt.addr}, newFakeCtl(hub), hub)
			appErr, ok := errors.FromError(err)
			if !ok {
				t.Fatalf("got %v, want a tagged error", err)
			}
			if appErr.Tag != TagListenFailed || !appErr.HasRoute(tt.route) || appErr.Metadata["step"] != tt.step {
				t.Errorf("tag %s route %s step %v", appErr.Tag, appErr.Route(), appErr.Metadata["step"])
			}
		})
	}

	t.Run("a UDP port in use", func(t *testing.T) {
		// Take a UDP port, then ask for a TCP port with the same number by
		// listening on it: find a free pair first.
		for range 20 {
			tcp, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			port := tcp.Addr().(*net.TCPAddr).Port
			udp, err := net.ListenPacket("udp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
			if err != nil {
				_ = tcp.Close()
				continue
			}
			_ = tcp.Close()
			defer func() { _ = udp.Close() }()
			hub := live.New()
			defer hub.Close()
			_, err = New(t.Context(), Config{Addr: net.JoinHostPort("127.0.0.1", strconv.Itoa(port))}, newFakeCtl(hub), hub)
			appErr, ok := errors.FromError(err)
			if !ok || appErr.Tag != TagListenFailed || appErr.Metadata["step"] != "udp" || !appErr.HasRoute("New->listenMedia.MediaListenFailed") {
				t.Fatalf("got %v", err)
			}
			return
		}
		t.Skip("no free pair of ports")
	})
}

func TestNewAPIFailure(t *testing.T) {
	bad := []codecSpec{{kind: webrtc.RTPCodecType(0), params: webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8, ClockRate: 90000}, PayloadType: 96,
	}}}
	_, err := newAPI(bad, webrtc.SettingEngine{})
	appErr, ok := errors.FromError(err)
	if !ok || appErr.Tag != TagSetupFailed || !appErr.HasRoute("newAPI.MediaSetupFailed") || appErr.Metadata["step"] != "codec" {
		t.Fatalf("got %v", err)
	}
	if _, err := newAPI(serverCodecs(), webrtc.SettingEngine{}); err != nil {
		t.Errorf("the codecs of the server: %+v", err)
	}
}

func TestServerCodecsAreOpusAndVP8Only(t *testing.T) {
	var got []string
	for _, c := range serverCodecs() {
		got = append(got, c.params.MimeType)
	}
	if strings.Join(got, ",") != webrtc.MimeTypeOpus+","+webrtc.MimeTypeVP8 {
		t.Errorf("codecs = %v", got)
	}
}

func TestWithDefaults(t *testing.T) {
	cfg := Config{}.withDefaults()
	if cfg.ICETimeout != 10*time.Second || cfg.DisconnectTimeout != 15*time.Second || cfg.ReconnectGrace != 15*time.Second ||
		cfg.MaxVideoBitrate != 2_500_000 || cfg.PingInterval != 30*time.Second || cfg.PingTimeout != 15*time.Second ||
		cfg.WriteTimeout != 10*time.Second || cfg.JoinTimeout != 30*time.Second || cfg.Logger == nil {
		t.Errorf("defaults = %+v", cfg)
	}
	set := Config{ICETimeout: time.Second, MaxVideoBitrate: 5}.withDefaults()
	if set.ICETimeout != time.Second || set.MaxVideoBitrate != 5 {
		t.Errorf("a value that is set was replaced: %+v", set)
	}
}

func TestPublicIPIsAnnouncedAsTheHostCandidate(t *testing.T) {
	r := newRig(t, Config{PublicIP: "127.0.0.1"})
	r.ctl.add(callID, callerID, calleeID, true)
	alice := r.dial("alice", r.url(callID, callerID, calleeID, callerID), callstest.Options{})
	callstest.WaitFor(t, "the server's candidates", 10*time.Second, func() bool { return len(alice.Candidates()) > 0 })
	candidates := alice.Candidates()
	for _, c := range candidates {
		if !strings.Contains(c, " 127.0.0.1 ") || !strings.Contains(c, "typ host") {
			t.Errorf("candidate %q is not the public IP as a host candidate", c)
		}
	}
	_, tcp := r.srv.Addr()
	if !strings.Contains(strings.Join(candidates, "\n"), strconv.Itoa(tcp.(*net.TCPAddr).Port)) {
		t.Errorf("candidates %v do not name the media port", candidates)
	}
}

// TestOnlyThisPackageImportsPion checks the rule of D63 and of CLAUDE.md: pion
// is imported only in internal/calls, which never imports the store.
func TestOnlyThisPackageImportsPion(t *testing.T) {
	root := filepath.Join("..", "..")
	fset := token.NewFileSet()
	var offenders []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".tmp", "data", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			t.Errorf("parse %s: %v", path, err)
			return nil
		}
		inCalls := strings.HasPrefix(filepath.ToSlash(path), "../../internal/calls/")
		for _, imp := range f.Imports {
			p := strings.Trim(imp.Path.Value, `"`)
			if strings.HasPrefix(p, "github.com/pion/") && !inCalls {
				offenders = append(offenders, path+" imports "+p)
			}
			if inCalls && strings.HasSuffix(p, "/internal/store") {
				offenders = append(offenders, path+" imports "+p)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(offenders) > 0 {
		t.Errorf("imports that break the rule:\n%s", strings.Join(offenders, "\n"))
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Errorf("the test did not find the module root: %v", err)
	}
}

func TestAttachFailuresAreTagged(t *testing.T) {
	r := newRig(t, Config{})
	r.ctl.add(callID, callerID, calleeID, true)
	alice := r.dial("alice", r.url(callID, callerID, calleeID, callerID), callstest.Options{})
	_ = alice
	var c *call
	callstest.WaitFor(t, "the call", 5*time.Second, func() bool {
		r.srv.mu.Lock()
		defer r.srv.mu.Unlock()
		c = r.srv.calls[callID]
		return c != nil
	})
	got := make(chan error, 2)
	c.post(func() { got <- c.attach(&socket{userID: 99}) })
	err := <-got
	appErr, ok := errors.FromError(err)
	if !ok || appErr.Tag != TagNotParticipant || !appErr.HasRoute("call.attach.MediaNotParticipant") {
		t.Errorf("a stranger: %v", err)
	}
	c.post(func() {
		c.ended = true
		got <- c.attach(&socket{userID: callerID})
		c.ended = false
	})
	err = <-got
	appErr, ok = errors.FromError(err)
	if !ok || appErr.Tag != TagCallClosed || !appErr.HasRoute("call.attach.MediaCallClosed") {
		t.Errorf("an ended call: %v", err)
	}
}

func TestAnOfferOnAClosedPeerConnectionIsTagged(t *testing.T) {
	r := newRig(t, Config{})
	r.ctl.add(callID, callerID, calleeID, true)
	r.dial("alice", r.url(callID, callerID, calleeID, callerID), callstest.Options{})
	var c *call
	callstest.WaitFor(t, "the call", 5*time.Second, func() bool {
		r.srv.mu.Lock()
		defer r.srv.mu.Unlock()
		c = r.srv.calls[callID]
		return c != nil && c.parts[0].cur.Load() != nil
	})
	got := make(chan error, 1)
	c.post(func() {
		pe := c.parts[0].cur.Load()
		if err := pe.pc.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
		got <- pe.sendOffer(false)
	})
	err := <-got
	appErr, ok := errors.FromError(err)
	if !ok || appErr.Tag != TagNegotiationFailed || !appErr.HasRoute("peer.sendOffer.MediaNegotiationFailed") || appErr.Metadata["step"] != "create offer" {
		t.Errorf("got %v", err)
	}
}
