// Copyright (C) 2026 Intel Corporation
// SPDX-License-Identifier: Apache-2.0

package nrfregistration

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omec-project/openapi/v2/models"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// These tests are about the end of a registration: what may still happen once the NF has been, or is being,
// deregistered.

// A registration or heartbeat that completes after the heartbeat timer was stopped, but before its context was
// cancelled, would arm a new timer. After an empty configuration the timer has to stay stopped.
//
// The window is a few instructions wide, so the test opens it: the logger calls a hook when the timer is
// stopped, and the hook lets a registration that is in flight (the recovery after a failed heartbeat) complete
// at that point. If the registration context was not cancelled before the timer was stopped, that registration
// arms a new timer.
func TestEmptyConfigDoesNotLeaveAHeartbeatArmedByARegistrationInFlight(t *testing.T) {
	for i := range 20 {
		t.Run(string(rune('a'+i)), func(t *testing.T) {
			var (
				registerCalls atomic.Int32
				updateCalls   atomic.Int32
				gate          = make(chan struct{})
				releaseOnce   sync.Once
				deregistered  = make(chan struct{})
			)
			profile := models.NFProfile{}
			profile.SetHeartBeatTimer(1)

			hooked := zap.New(
				zapcore.NewCore(zapcore.NewConsoleEncoder(zap.NewDevelopmentEncoderConfig()), zapcore.AddSync(io.Discard), zap.DebugLevel),
				zap.Hooks(func(e zapcore.Entry) error {
					if e.Message == "stopped heartbeat timer" {
						releaseOnce.Do(func() { close(gate) })
						time.Sleep(30 * time.Millisecond) // let the registration reach the timer
					}
					return nil
				}),
			).Sugar()

			s, err := New(Config[[]string]{
				NfName:  "TEST",
				Log:     hooked,
				IsEmpty: func(c []string) bool { return len(c) == 0 },
				Register: func(ctx context.Context, _ []string) (*models.NFProfile, error) {
					if registerCalls.Add(1) == 1 {
						return &profile, nil
					}
					// the recovery registration: the request completes when the gate opens, or fails
					// when its context is cancelled
					select {
					case <-gate:
						return &profile, nil
					case <-ctx.Done():
						return nil, ctx.Err()
					}
				},
				Update: func(context.Context, []models.PatchItem) (*models.NFProfile, *models.ProblemDetails, error) {
					updateCalls.Add(1)
					return nil, nil, errors.New("heartbeat failed")
				},
				Deregister: func(context.Context) error {
					close(deregistered)
					return nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			ft := &fakeTimers{}
			s.afterFunc = ft.afterFunc
			ch := make(chan []string)
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() { defer close(done); s.Start(ctx, ch) }()
			t.Cleanup(func() { cancel(); <-done })

			ch <- []string{"a"}
			waitFor(t, func() bool { return ft.count() == 1 }, "heartbeat timer was not armed")

			// the heartbeat fails and the recovery registration is in flight
			go ft.fire(t)
			waitFor(t, func() bool { return registerCalls.Load() == 2 }, "recovery registration did not start")

			ch <- nil // empty configuration: deregister
			select {
			case <-deregistered:
			case <-time.After(3 * time.Second):
				t.Fatal("the NF was not deregistered")
			}
			time.Sleep(30 * time.Millisecond) // time for a registration to arm a timer, if it would

			if got := ft.count(); got != 1 {
				t.Errorf("heartbeat timers armed = %d, want only the first: a registration armed one after the deregistration", got)
			}
			s.keepAliveTimerMutex.Lock()
			armed := s.keepAliveTimer != nil
			s.keepAliveTimerMutex.Unlock()
			if armed {
				t.Error("a heartbeat timer is armed after the deregistration")
			}
		})
	}
}

// Deregister is terminal. The Start loop keeps running until its context is done, and a configuration it
// receives after Deregister must not register the NF: no heartbeat is sent any more, so the registration
// could not be kept alive. Only a new Start registers again.
func TestConfigAfterDeregisterDoesNotRegisterTheNf(t *testing.T) {
	f := &fakeNrf{}
	s, ft, ch, stop := startService(t, f.config())

	ch <- []string{"a"}
	waitFor(t, func() bool { return ft.count() == 1 }, "heartbeat timer was not armed")

	s.Deregister(context.Background())
	if f.deregisterCall.Load() != 1 {
		t.Fatalf("deregister calls = %d, want 1", f.deregisterCall.Load())
	}

	ch <- []string{"b"} // the Start loop is still running
	time.Sleep(50 * time.Millisecond)
	if got := f.registers(); got != 1 {
		t.Errorf("register calls = %d, want 1: the NF was registered after it was deregistered", got)
	}
	if got := ft.count(); got != 1 {
		t.Errorf("heartbeat timers armed = %d, want 1", got)
	}

	// a new Start enables registration again
	stop()
	ch2 := make(chan []string)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); s.Start(ctx, ch2) }()
	t.Cleanup(func() { cancel(); <-done })
	ch2 <- []string{"c"}
	waitFor(t, func() bool { return f.registers() == 2 && ft.count() == 2 }, "a new Start did not register the NF again")
}

// A registration that is already in flight when Deregister is called is waited for, and the deregistration
// follows it: the NRF ends with no registration, not with one the NF cannot keep alive.
func TestDeregisterWaitsForARegistrationInFlight(t *testing.T) {
	f := &fakeNrf{registerBlock: make(chan struct{})}
	s, ft, ch, _ := startService(t, f.config())

	ch <- []string{"a"}
	waitFor(t, func() bool { f.mu.Lock(); defer f.mu.Unlock(); return len(f.ctxs) == 1 }, "Register was not called")

	deregistered := make(chan struct{})
	go func() { s.Deregister(context.Background()); close(deregistered) }()
	f.firstCtxCancelled(t)
	select {
	case <-deregistered:
		t.Fatal("Deregister returned while the registration was still in flight")
	case <-time.After(30 * time.Millisecond):
	}

	close(f.registerBlock) // the registration completes
	select {
	case <-deregistered:
	case <-time.After(2 * time.Second):
		t.Fatal("Deregister did not return")
	}
	if f.deregisterCall.Load() != 1 {
		t.Errorf("deregister calls = %d, want 1", f.deregisterCall.Load())
	}
	if got := ft.count(); got != 0 {
		t.Errorf("a heartbeat timer was armed by the registration that was cancelled: %d", got)
	}
}

// A heartbeat timer that fires after its registration was superseded must not send a heartbeat for it: the
// newer registration owns the NF's heartbeat.
func TestHeartbeatOfASupersededRegistrationIsNotSent(t *testing.T) {
	f := &fakeNrf{}
	_, ft, ch, _ := startService(t, f.config())

	ch <- []string{"a"}
	waitFor(t, func() bool { return ft.count() == 1 }, "heartbeat timer was not armed")

	f.registerBlock = make(chan struct{}) // the new registration stays in flight
	ch <- []string{"b"}                   // supersedes the first registration
	f.firstCtxCancelled(t)

	ft.fire(t) // the first registration's timer fires after all
	if got := f.updateCalls.Load(); got != 0 {
		t.Errorf("heartbeats sent = %d, want none for a superseded registration", got)
	}
	close(f.registerBlock)
}
