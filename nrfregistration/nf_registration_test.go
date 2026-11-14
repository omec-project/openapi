// Copyright (C) 2026 Intel Corporation
// SPDX-FileCopyrightText: 2025 Canonical Ltd.
// SPDX-License-Identifier: Apache-2.0

package nrfregistration

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omec-project/openapi/v2/models"
)

type fakeNrf struct {
	mu             sync.Mutex
	registerErrs   []error // consumed one per Register call; nil afterwards means success
	registerCfgs   [][]string
	heartbeat      int32 // profile heartbeat timer returned by Register
	updateErr      error
	updateProblem  *models.ProblemDetails
	updateCalls    atomic.Int32
	deregisterCall atomic.Int32
	deregisterErr  error
	registerBlock  chan struct{} // when set, Register waits on it before returning
	ctxs           []context.Context
}

func (f *fakeNrf) config() Config[[]string] {
	return Config[[]string]{
		NfName:    "TEST",
		RetryTime: 5 * time.Millisecond,
		IsEmpty:   func(c []string) bool { return len(c) == 0 },
		Register: func(ctx context.Context, c []string) (*models.NFProfile, error) {
			f.mu.Lock()
			f.ctxs = append(f.ctxs, ctx)
			f.mu.Unlock()
			if f.registerBlock != nil {
				<-f.registerBlock
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			f.registerCfgs = append(f.registerCfgs, c)
			if len(f.registerErrs) > 0 {
				err := f.registerErrs[0]
				f.registerErrs = f.registerErrs[1:]
				if err != nil {
					return nil, err
				}
			}
			p := models.NFProfile{}
			if f.heartbeat != 0 {
				p.SetHeartBeatTimer(f.heartbeat)
			}
			return &p, nil
		},
		Update: func(ctx context.Context, patch []models.PatchItem) (*models.NFProfile, *models.ProblemDetails, error) {
			f.updateCalls.Add(1)
			return nil, f.updateProblem, f.updateErr
		},
		Deregister: func(ctx context.Context) error {
			f.deregisterCall.Add(1)
			return f.deregisterErr
		},
	}
}

// firstCtxCancelled waits for the first Register call to start and its context to be cancelled.
func (f *fakeNrf) firstCtxCancelled(t *testing.T) {
	t.Helper()
	waitFor(t, func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return len(f.ctxs) > 0 && f.ctxs[0].Err() != nil
	}, "first registration context was not cancelled")
}

func (f *fakeNrf) registers() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.registerCfgs)
}

// fakeTimers captures AfterFunc calls so tests fire heartbeats by hand.
type fakeTimers struct {
	mu        sync.Mutex
	durations []time.Duration
	funcs     []func()
}

func (ft *fakeTimers) afterFunc(d time.Duration, f func()) *time.Timer {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	ft.durations = append(ft.durations, d)
	ft.funcs = append(ft.funcs, f)
	return time.NewTimer(time.Hour)
}

func (ft *fakeTimers) count() int {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	return len(ft.funcs)
}

func (ft *fakeTimers) fire(t *testing.T) {
	t.Helper()
	ft.mu.Lock()
	f := ft.funcs[0]
	ft.mu.Unlock()
	f()
}

func waitFor(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal(msg)
}

func startService(t *testing.T, cfg Config[[]string]) (*Service[[]string], *fakeTimers, chan []string, func()) {
	t.Helper()
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ft := &fakeTimers{}
	s.afterFunc = ft.afterFunc
	ch := make(chan []string)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Start(ctx, ch)
	}()
	stop := func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("Start did not return after context cancel")
		}
	}
	t.Cleanup(stop)
	return s, ft, ch, stop
}

func TestNew_ValidatesRequiredFields(t *testing.T) {
	f := &fakeNrf{}
	full := f.config()
	cases := map[string]func(c *Config[[]string]){
		"NfName":     func(c *Config[[]string]) { c.NfName = "" },
		"Register":   func(c *Config[[]string]) { c.Register = nil },
		"Update":     func(c *Config[[]string]) { c.Update = nil },
		"Deregister": func(c *Config[[]string]) { c.Deregister = nil },
		"IsEmpty":    func(c *Config[[]string]) { c.IsEmpty = nil },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			c := full
			mutate(&c)
			if _, err := New(c); err == nil {
				t.Fatalf("expected error when %s is missing", name)
			}
		})
	}
	if _, err := New(full); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestStart_NonEmptyConfig_RegistersAndArmsHeartbeatWithProfileTimer(t *testing.T) {
	f := &fakeNrf{heartbeat: 30}
	_, ft, ch, _ := startService(t, f.config())

	ch <- []string{"a"}
	waitFor(t, func() bool { return ft.count() == 1 }, "heartbeat timer was not armed")
	if ft.durations[0] != 30*time.Second {
		t.Errorf("heartbeat duration = %v, want 30s", ft.durations[0])
	}
}

func TestStart_ProfileWithoutHeartbeat_UsesDefault(t *testing.T) {
	f := &fakeNrf{}
	_, ft, ch, _ := startService(t, f.config())

	ch <- []string{"a"}
	waitFor(t, func() bool { return ft.count() == 1 }, "heartbeat timer was not armed")
	if ft.durations[0] != time.Duration(defaultHeartbeatTimer)*time.Second {
		t.Errorf("heartbeat duration = %v, want default", ft.durations[0])
	}
}

func TestStart_RegisterFails_RetriesUntilSuccess(t *testing.T) {
	f := &fakeNrf{registerErrs: []error{errors.New("boom"), errors.New("boom")}}
	_, ft, ch, _ := startService(t, f.config())

	ch <- []string{"a"}
	waitFor(t, func() bool { return ft.count() == 1 }, "heartbeat timer was not armed after retries")
	if got := f.registers(); got != 3 {
		t.Errorf("register calls = %d, want 3", got)
	}
}

func TestStart_EmptyConfig_DeregistersAndStopsHeartbeat(t *testing.T) {
	for _, derr := range []error{nil, errors.New("nrf down")} {
		f := &fakeNrf{deregisterErr: derr}
		s, ft, ch, _ := startService(t, f.config())

		ch <- []string{"a"}
		waitFor(t, func() bool { return ft.count() == 1 }, "heartbeat timer was not armed")
		ch <- nil
		waitFor(t, func() bool { return f.deregisterCall.Load() == 1 }, "deregister was not called")

		s.keepAliveTimerMutex.Lock()
		armed := s.keepAliveTimer != nil
		s.keepAliveTimerMutex.Unlock()
		if armed {
			t.Errorf("deregister error=%v: keepalive timer still armed", derr)
		}
	}
}

func TestStart_RegisterAfterDeregister_ArmsHeartbeatAgain(t *testing.T) {
	f := &fakeNrf{}
	_, ft, ch, _ := startService(t, f.config())

	ch <- []string{"a"}
	waitFor(t, func() bool { return ft.count() == 1 }, "first heartbeat not armed")
	ch <- nil
	ch <- []string{"b"}
	waitFor(t, func() bool { return ft.count() == 2 }, "heartbeat not re-armed after deregister")
}

func TestStart_NewConfigSupersedesInFlightRegistration(t *testing.T) {
	f := &fakeNrf{registerBlock: make(chan struct{})}
	_, ft, ch, _ := startService(t, f.config())

	ch <- []string{"old"}
	waitFor(t, func() bool { f.mu.Lock(); defer f.mu.Unlock(); return len(f.ctxs) == 1 }, "Register was not called")
	ch <- []string{"new"} // cancels the first registration, which is still blocked in Register
	f.firstCtxCancelled(t)
	close(f.registerBlock)

	waitFor(t, func() bool { return ft.count() >= 1 }, "heartbeat timer was not armed")
	time.Sleep(20 * time.Millisecond)
	if got := ft.count(); got != 1 {
		t.Errorf("heartbeat timers armed = %d, want 1 (superseded registration must not arm)", got)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if last := f.registerCfgs[len(f.registerCfgs)-1]; last[0] != "new" {
		t.Errorf("last registered config = %v, want new", last)
	}
}

func TestStart_EmptyConfigCancelsInFlightRegistration(t *testing.T) {
	f := &fakeNrf{registerBlock: make(chan struct{})}
	_, ft, ch, _ := startService(t, f.config())

	ch <- []string{"a"}
	waitFor(t, func() bool { f.mu.Lock(); defer f.mu.Unlock(); return len(f.ctxs) == 1 }, "Register was not called")
	ch <- nil
	f.firstCtxCancelled(t)
	close(f.registerBlock)

	time.Sleep(30 * time.Millisecond)
	if got := ft.count(); got != 0 {
		t.Errorf("heartbeat armed after deregistration: %d timers", got)
	}
}

func TestStart_ChannelClosed_StopsService(t *testing.T) {
	f := &fakeNrf{}
	s, err := New(f.config())
	if err != nil {
		t.Fatal(err)
	}
	ch := make(chan []string)
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Start(context.Background(), ch)
	}()
	close(ch)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return after channel close")
	}
}

func TestStart_ContextDone_StopsHeartbeat(t *testing.T) {
	f := &fakeNrf{}
	s, ft, ch, stop := startService(t, f.config())

	ch <- []string{"a"}
	waitFor(t, func() bool { return ft.count() == 1 }, "heartbeat timer was not armed")
	stop()

	s.keepAliveTimerMutex.Lock()
	defer s.keepAliveTimerMutex.Unlock()
	if s.keepAliveTimer != nil || !s.keepAliveShutdown {
		t.Error("expected keepalive timer stopped and shutdown flag set")
	}
}

func TestHeartbeat_Success_NoReregisterAndRearms(t *testing.T) {
	f := &fakeNrf{}
	_, ft, ch, _ := startService(t, f.config())

	ch <- []string{"a"}
	waitFor(t, func() bool { return ft.count() == 1 }, "heartbeat timer was not armed")
	ft.fire(t)

	if f.updateCalls.Load() != 1 {
		t.Errorf("update calls = %d, want 1", f.updateCalls.Load())
	}
	if f.registers() != 1 {
		t.Errorf("register calls = %d, want 1", f.registers())
	}
	if ft.count() != 2 {
		t.Errorf("timers armed = %d, want 2", ft.count())
	}
}

func TestHeartbeat_Failure_ReregistersAndRearms(t *testing.T) {
	cases := map[string]func(f *fakeNrf){
		"error":   func(f *fakeNrf) { f.updateErr = errors.New("boom") },
		"problem": func(f *fakeNrf) { f.updateProblem = &models.ProblemDetails{} },
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			f := &fakeNrf{}
			_, ft, ch, _ := startService(t, f.config())

			ch <- []string{"a"}
			waitFor(t, func() bool { return ft.count() == 1 }, "heartbeat timer was not armed")
			setup(f)
			ft.fire(t)

			if f.registers() != 2 {
				t.Errorf("register calls = %d, want 2", f.registers())
			}
			if ft.count() != 2 {
				t.Errorf("timers armed = %d, want 2", ft.count())
			}
		})
	}
}

func TestHeartbeat_AfterStop_IsNoop(t *testing.T) {
	f := &fakeNrf{}
	_, ft, ch, _ := startService(t, f.config())

	ch <- []string{"a"}
	waitFor(t, func() bool { return ft.count() == 1 }, "heartbeat timer was not armed")
	ch <- nil
	waitFor(t, func() bool { return f.deregisterCall.Load() == 1 }, "deregister was not called")
	ft.fire(t)

	if f.updateCalls.Load() != 0 {
		t.Error("heartbeat sent after deregistration")
	}
}

func TestDeregister_PreventsLateRegistrationFromArmingHeartbeat(t *testing.T) {
	f := &fakeNrf{registerBlock: make(chan struct{})}
	s, ft, ch, _ := startService(t, f.config())

	ch <- []string{"a"}
	waitFor(t, func() bool { f.mu.Lock(); defer f.mu.Unlock(); return len(f.ctxs) == 1 }, "register was not called")
	// Register ignores cancellation here, so Deregister must wait for it before sending the DELETE
	go func() {
		time.Sleep(20 * time.Millisecond)
		close(f.registerBlock)
	}()
	s.Deregister(context.Background()) // as on NF termination; registration still in flight
	if f.registers() != 1 {
		t.Errorf("Deregister returned before the in-flight registration completed")
	}

	time.Sleep(30 * time.Millisecond)
	if ft.count() != 0 {
		t.Error("heartbeat armed after Deregister")
	}
	if f.deregisterCall.Load() != 1 {
		t.Errorf("deregister calls = %d, want 1", f.deregisterCall.Load())
	}
}

func TestStart_RequiresRegistration_SkipsUnchangedConfig(t *testing.T) {
	f := &fakeNrf{}
	cfg := f.config()
	cfg.RequiresRegistration = func(n, o []string) bool { return len(n) != len(o) }
	_, ft, ch, _ := startService(t, cfg)

	ch <- []string{"a"}
	waitFor(t, func() bool { return ft.count() == 1 }, "heartbeat timer was not armed")
	ch <- []string{"b"} // same length: skipped
	time.Sleep(20 * time.Millisecond)
	if f.registers() != 1 {
		t.Errorf("register calls = %d, want 1", f.registers())
	}
	ch <- []string{"b", "c"}
	waitFor(t, func() bool { return f.registers() == 2 }, "changed config was not registered")

	ch <- nil // deregistration forgets the last config, so the next one always registers
	ch <- []string{"x", "y"}
	waitFor(t, func() bool { return f.registers() == 3 }, "config after deregistration was not registered")
}

func TestStart_Clone_ProtectsLastConfigFromMutation(t *testing.T) {
	f := &fakeNrf{}
	cfg := f.config()
	cfg.RequiresRegistration = func(n, o []string) bool { return n[0] != o[0] }
	cfg.Clone = func(c []string) ([]string, error) { return append([]string(nil), c...), nil }
	_, ft, ch, _ := startService(t, cfg)

	shared := []string{"a"}
	ch <- shared
	waitFor(t, func() bool { return ft.count() == 1 }, "heartbeat timer was not armed")
	shared[0] = "b" // caller mutates in place and re-sends
	ch <- shared
	waitFor(t, func() bool { return f.registers() == 2 }, "mutation was not detected as a change")
}

func TestStart_CloneError_SkipsRegistration(t *testing.T) {
	f := &fakeNrf{}
	cfg := f.config()
	cfg.Clone = func([]string) ([]string, error) { return nil, errors.New("nope") }
	_, _, ch, stop := startService(t, cfg)
	_ = stop

	ch <- []string{"a"}
	time.Sleep(20 * time.Millisecond)
	if f.registers() != 0 {
		t.Error("registered despite clone failure")
	}
}

func TestStart_OnConfig_CalledForNonEmptyConfigs(t *testing.T) {
	f := &fakeNrf{}
	cfg := f.config()
	var calls atomic.Int32
	cfg.OnConfig = func([]string) { calls.Add(1) }
	_, _, ch, stop := startService(t, cfg)
	_ = stop

	ch <- []string{"a"}
	ch <- nil
	ch <- []string{"b"}
	waitFor(t, func() bool { return calls.Load() == 2 }, "OnConfig was not called for both non-empty configs")
}

func TestHeartbeat_EmptyResponse_KeepsNegotiatedInterval(t *testing.T) {
	f := &fakeNrf{heartbeat: 30}
	_, ft, ch, _ := startService(t, f.config())

	ch <- []string{"a"}
	waitFor(t, func() bool { return ft.count() == 1 }, "heartbeat timer was not armed")
	ft.fire(t) // Update returns no profile, as on HTTP 204

	if ft.count() != 2 {
		t.Fatalf("timers armed = %d, want 2", ft.count())
	}
	ft.mu.Lock()
	defer ft.mu.Unlock()
	for i, d := range ft.durations {
		if d != 30*time.Second {
			t.Errorf("timer %d = %v, want 30s", i, d)
		}
	}
}

func TestHeartbeat_Failure_RetriesRegistrationUntilSuccess(t *testing.T) {
	f := &fakeNrf{}
	_, ft, ch, _ := startService(t, f.config())

	ch <- []string{"a"}
	waitFor(t, func() bool { return ft.count() == 1 }, "heartbeat timer was not armed")
	f.mu.Lock()
	f.registerErrs = []error{errors.New("nrf down"), errors.New("nrf down")}
	f.mu.Unlock()
	f.updateErr = errors.New("boom")
	ft.fire(t)

	if f.registers() != 4 {
		t.Errorf("register calls = %d, want 4 (initial + 2 failed + 1 recovered)", f.registers())
	}
	if ft.count() != 2 {
		t.Errorf("timers armed = %d, want 2", ft.count())
	}
}

func TestStart_RegistrationContextInheritsCallerContext(t *testing.T) {
	type key struct{}
	f := &fakeNrf{}
	s, err := New(f.config())
	if err != nil {
		t.Fatal(err)
	}
	s.afterFunc = (&fakeTimers{}).afterFunc
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), key{}, "token"))
	ch := make(chan []string)
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Start(ctx, ch)
	}()
	defer func() {
		cancel()
		<-done
	}()

	ch <- []string{"a"}
	waitFor(t, func() bool { return f.registers() == 1 }, "register was not called")
	f.mu.Lock()
	defer f.mu.Unlock()
	if got := f.ctxs[0].Value(key{}); got != "token" {
		t.Errorf("registration context value = %v, want token", got)
	}
}

func TestDeregister_StopsRegistrationRetries(t *testing.T) {
	f := &fakeNrf{}
	for range 1000 {
		f.registerErrs = append(f.registerErrs, errors.New("nrf down"))
	}
	s, ft, ch, _ := startService(t, f.config())

	ch <- []string{"a"}
	waitFor(t, func() bool { return f.registers() >= 1 }, "register was not attempted")
	s.Deregister(context.Background())
	after := f.registers()

	time.Sleep(30 * time.Millisecond) // several RetryTime periods
	if got := f.registers(); got != after {
		t.Errorf("register calls after Deregister = %d, want %d", got, after)
	}
	if ft.count() != 0 {
		t.Error("heartbeat armed after Deregister")
	}
}

func TestStart_EmptyConfig_StopsRegistrationRetries(t *testing.T) {
	f := &fakeNrf{}
	for range 1000 {
		f.registerErrs = append(f.registerErrs, errors.New("nrf down"))
	}
	_, ft, ch, _ := startService(t, f.config())

	ch <- []string{"a"}
	waitFor(t, func() bool { return f.registers() >= 1 }, "register was not attempted")
	ch <- nil
	waitFor(t, func() bool { return f.deregisterCall.Load() == 1 }, "deregister was not called")
	after := f.registers()

	time.Sleep(30 * time.Millisecond)
	if got := f.registers(); got != after {
		t.Errorf("register calls after deregistration = %d, want %d", got, after)
	}
	if ft.count() != 0 {
		t.Error("heartbeat armed after deregistration")
	}
}

func TestStartStatic_RegistersOnceAndKeepsAlive(t *testing.T) {
	f := &fakeNrf{heartbeat: 30}
	s, err := New(f.config())
	if err != nil {
		t.Fatal(err)
	}
	ft := &fakeTimers{}
	s.afterFunc = ft.afterFunc
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); s.StartStatic(ctx, []string{"static"}) }()

	waitFor(t, func() bool { return ft.count() == 1 }, "heartbeat timer was not armed")
	if f.registers() != 1 {
		t.Errorf("register calls = %d, want 1", f.registers())
	}
	ft.fire(t)
	if f.updateCalls.Load() != 1 {
		t.Errorf("heartbeat update calls = %d, want 1", f.updateCalls.Load())
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("StartStatic did not return after ctx was cancelled")
	}
	s.Deregister(context.Background())
	if f.deregisterCall.Load() != 1 {
		t.Errorf("deregister calls = %d, want 1", f.deregisterCall.Load())
	}
}
