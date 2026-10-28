package main

import (
	"context"
	"encoding/hex"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/calls"
	"github.com/rm4n0s/clowder/internal/cluster"
	"github.com/rm4n0s/clowder/internal/config"
	"github.com/rm4n0s/clowder/internal/live"
	"github.com/rm4n0s/errors"
)

const (
	// callExpireInterval is how often serve ends the calls that have rung for
	// 45 seconds (SPEC §7.18).
	callExpireInterval = time.Second
	// callShutdownTimeout bounds the work that ends the calls of the process
	// when it stops.
	callShutdownTimeout = 10 * time.Second
)

// newProcessID makes the ID of this process: 16 random hexadecimal characters.
// A call records it as its home (SPEC D65).
func newProcessID(random io.Reader) (string, error) {
	var b [8]byte
	if _, err := io.ReadFull(random, b[:]); err != nil {
		return "", errors.NewErr(tagProcessIDFailed, err)
	}
	return hex.EncodeToString(b[:]), nil
}

// callsController gives the media server what it asks of the service of the
// calls (calls.Controller). The media server has checked nothing about users:
// the handlers did, before the socket joined the call.
type callsController struct {
	svc *app.Calls
}

// Status reads the state of the call.
func (c callsController) Status(ctx context.Context, callID uint64) (calls.Status, error) {
	st, err := c.svc.Status(ctx, callID)
	if err != nil {
		return calls.Status{}, err
	}
	return calls.Status{Open: st.Open, Active: st.Active, Reason: string(st.Reason)}, nil
}

// Fail ends the call as failed.
func (c callsController) Fail(ctx context.Context, callID uint64) error {
	return c.svc.Fail(ctx, callID)
}

// SetMedia tells both users about the microphone and the camera of a
// participant.
func (c callsController) SetMedia(ctx context.Context, callID, userID uint64, microphone, camera bool) error {
	return c.svc.SetMedia(ctx, app.Actor{UserID: userID}, callID, microphone, camera)
}

// Leave ends the call as the participant.
func (c callsController) Leave(ctx context.Context, callID, userID uint64) error {
	return c.svc.Leave(ctx, app.Actor{UserID: userID}, callID)
}

// callExpirer ends the calls that rang too long and the calls whose home
// process is gone, and says how many it ended. *app.Calls is one.
type callExpirer interface {
	Expire(ctx context.Context) (int, error)
	EndOrphans(ctx context.Context) (int, error)
}

// startCallExpirer calls Expire and EndOrphans every interval, in a goroutine
// that ctx stops. The returned function stops the goroutine, waits for it to
// end and may be called more than once. A failure is logged and tried again at
// the next tick.
func startCallExpirer(ctx context.Context, ex callExpirer, interval time.Duration, logger *slog.Logger) func() {
	ctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			n, err := ex.Expire(ctx)
			switch {
			case ctx.Err() != nil:
				return
			case err != nil:
				logger.Error("ending the calls that rang too long failed", "err", err)
			case n > 0:
				logger.Info("calls were not answered", "count", n)
			}
			n, err = ex.EndOrphans(ctx)
			switch {
			case ctx.Err() != nil:
				return
			case err != nil:
				logger.Error("ending the calls of processes that are gone failed", "err", err)
			case n > 0:
				logger.Info("calls were ended because their process is gone", "count", n)
			}
		}
	}()
	return func() {
		cancel()
		wg.Wait()
	}
}

// callStopper ends open calls as server_stopped: those of a process, or all of
// them when the process is empty. *app.Calls is one.
type callStopper interface {
	EndOpen(ctx context.Context, home string) (int, error)
}

// endCallsAtStart ends every call that is not over when a process without Redis
// starts: none of them can be live, because the media of a call lives in the
// process that started it and no other process runs (SPEC D65). With Redis
// other processes may be running, and the loop of startCallExpirer ends the
// calls whose home has no live key (Calls.EndOrphans).
func endCallsAtStart(ctx context.Context, svc callStopper, cfg config.Config, logger *slog.Logger) error {
	if cfg.RedisURL != "" {
		return nil
	}
	n, err := svc.EndOpen(ctx, "")
	if err != nil {
		return err
	}
	if n > 0 {
		logger.Info("calls of an earlier run were ended", "count", n)
	}
	return nil
}

// endCallsAtStop ends the calls of this process as server_stopped when it stops
// gracefully, with a context of its own: the one of serve is done already.
func endCallsAtStop(ctx context.Context, svc callStopper, processID string, logger *slog.Logger) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), callShutdownTimeout)
	defer cancel()
	n, err := svc.EndOpen(ctx, processID)
	if err != nil {
		logger.Error("ending the calls of this process failed", "err", err)
		return
	}
	if n > 0 {
		logger.Info("calls were ended because the server stops", "count", n)
	}
}

// callLink is what the calls of this process share with the other processes
// through Redis (SPEC D65): the relay of the signalling, the announcement that
// the process is alive and the check whether another one is. *cluster.CallLink
// is one.
type callLink interface {
	calls.Relay
	app.ProcessDirectory
	// Announce writes the key of the process, with its media address, and keeps
	// it alive until Close.
	Announce(ctx context.Context, addr string) error
	// Close removes the key and closes the connection.
	Close() error
}

// callLinkOpener opens the link to Redis; openCallLink is the real one and
// tests pass a fake.
type callLinkOpener func(ctx context.Context, redisURL, processID string, logger *slog.Logger) (callLink, error)

// openCallLink opens a cluster.CallLink.
func openCallLink(ctx context.Context, redisURL, processID string, logger *slog.Logger) (callLink, error) {
	l, err := cluster.NewCallLink(ctx, cluster.CallLinkConfig{RedisURL: redisURL, ProcessID: processID, Logger: logger})
	if err != nil {
		return nil, err
	}
	return l, nil
}

// newCallLink opens the link of the calls when redisURL is set, and returns nil
// (and a function that does nothing) when it is not: a process that runs alone
// has no one to share calls with. The returned function removes the key of the
// process from Redis and closes the link; serve calls it after the calls of the
// process were ended.
func newCallLink(ctx context.Context, redisURL, processID string, logger *slog.Logger, open callLinkOpener) (callLink, func(), error) {
	if redisURL == "" {
		return nil, func() {}, nil
	}
	link, err := open(ctx, redisURL, processID, logger)
	if err != nil {
		return nil, nil, err
	}
	logger.Info("calls are shared through Redis", "redis", config.RedactURL(redisURL), "process", processID)
	return link, func() {
		if err := link.Close(); err != nil {
			logger.Error("closing the link of the calls failed", "err", err)
		}
	}, nil
}

// announcedAddr is the media address that a process announces in Redis: the
// public IP when it is set, else the address it listens on, with the port of
// the UDP socket.
func announcedAddr(publicIP string, udp net.Addr) string {
	host, port, err := net.SplitHostPort(udp.String())
	if err != nil {
		return udp.String()
	}
	if publicIP != "" {
		host = publicIP
	}
	return net.JoinHostPort(host, port)
}

// startMedia starts the media server of the calls when cfg.MediaAddr is set,
// and returns it with the function that stops it (nil and a function that does
// nothing when calls are off). The server stops when the function is called.
// With a link (several processes on Redis) the server relays the sockets of the
// calls that other processes hold, and the process announces its media address.
func startMedia(ctx context.Context, cfg config.Config, svc *app.Calls, hub *live.Hub, processID string, link callLink, logger *slog.Logger) (*calls.Server, func(), error) {
	if cfg.MediaAddr == "" {
		return nil, func() {}, nil
	}
	mediaCfg := calls.Config{Addr: cfg.MediaAddr, PublicIP: cfg.MediaPublicIP, ProcessID: processID, Logger: logger}
	if link != nil {
		mediaCfg.Relay = link
	}
	media, err := calls.New(ctx, mediaCfg, callsController{svc: svc}, hub)
	if err != nil {
		return nil, nil, err
	}
	stop := func() {
		if err := media.Close(); err != nil {
			logger.Error("closing the media server failed", "err", err)
		}
	}
	udp, tcp := media.Addr()
	logger.Info("media server listening", "udp", udp.String(), "tcp", tcp.String(), "public_ip", cfg.MediaPublicIP)
	if link != nil {
		if err := link.Announce(ctx, announcedAddr(cfg.MediaPublicIP, udp)); err != nil {
			stop()
			return nil, nil, err
		}
	}
	return media, stop, nil
}

const tagProcessIDFailed = "ProcessIDFailed"
