package main

import (
	"context"
	"encoding/hex"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/calls"
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

// callExpirer ends the calls that rang too long and says how many it ended.
// *app.Calls is one.
type callExpirer interface {
	Expire(ctx context.Context) (int, error)
}

// startCallExpirer calls Expire every interval, in a goroutine that ctx stops.
// The returned function stops the goroutine, waits for it to end and may be
// called more than once. A failure is logged and tried again at the next tick.
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
// process that started it and no other process runs (SPEC D65).
//
// TODO(step 22.3): with Redis, end only the calls whose home process is dead.
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

// startMedia starts the media server of the calls when cfg.MediaAddr is set,
// and returns it with the function that stops it (nil and a function that does
// nothing when calls are off). The server stops when the function is called.
func startMedia(ctx context.Context, cfg config.Config, svc *app.Calls, hub *live.Hub, logger *slog.Logger) (*calls.Server, func(), error) {
	if cfg.MediaAddr == "" {
		return nil, func() {}, nil
	}
	media, err := calls.New(ctx, calls.Config{Addr: cfg.MediaAddr, PublicIP: cfg.MediaPublicIP, Logger: logger},
		callsController{svc: svc}, hub)
	if err != nil {
		return nil, nil, err
	}
	udp, tcp := media.Addr()
	logger.Info("media server listening", "udp", udp.String(), "tcp", tcp.String(), "public_ip", cfg.MediaPublicIP)
	return media, func() {
		if err := media.Close(); err != nil {
			logger.Error("closing the media server failed", "err", err)
		}
	}, nil
}

const tagProcessIDFailed = "ProcessIDFailed"
