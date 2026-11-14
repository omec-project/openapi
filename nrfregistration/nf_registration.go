// Copyright (C) 2024-Present Intel Corporation
// SPDX-FileCopyrightText: 2025 Canonical Ltd
// SPDX-FileCopyrightText: 2021 Open Networking Foundation <info@opennetworking.org>
// Copyright 2019 free5GC.org
// SPDX-License-Identifier: Apache-2.0

// Package nrfregistration implements the NF registration lifecycle shared by the network functions:
// register to the NRF when a non-empty configuration arrives, keep the registration alive with
// heartbeats, re-register when a heartbeat fails, and deregister when the configuration becomes empty.
package nrfregistration

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/omec-project/openapi/v2/logger"
	"github.com/omec-project/openapi/v2/models"
	"go.uber.org/zap"
)

const (
	defaultHeartbeatTimer int32 = 60
	defaultRetryTime            = 10 * time.Second
)

// Config describes how a network function plugs into the registration service. C is the type of the
// dynamic configuration that drives the NF profile (e.g. []models.PlmnId).
type Config[C any] struct {
	// NfName is used in log messages, e.g. "UDM".
	NfName string
	// Log is optional. It defaults to logger.NrfRegistrationLog.
	Log *zap.SugaredLogger
	// RetryTime is the interval between failed registration attempts. Defaults to 10 seconds.
	RetryTime time.Duration

	// Register sends a RegisterNFInstance. Required.
	Register func(ctx context.Context, cfg C) (*models.NFProfile, error)
	// Update sends an UpdateNFInstance (used for heartbeats). Required.
	Update func(ctx context.Context, patch []models.PatchItem) (*models.NFProfile, *models.ProblemDetails, error)
	// Deregister sends a DeregisterNFInstance. Required.
	Deregister func(ctx context.Context) error

	// IsEmpty reports whether cfg means the NF must be deregistered. Required.
	IsEmpty func(cfg C) bool
	// RequiresRegistration is optional. When set, a non-empty config is only (re-)registered if it
	// returns true for the new config and the last config that was sent for registration. The first
	// config after start, or after a deregistration, always registers.
	RequiresRegistration func(newCfg, lastCfg C) bool
	// Clone is optional. When set, the copy it returns is what is kept as the last registered config,
	// which protects RequiresRegistration from callers that mutate configs after sending them.
	Clone func(cfg C) (C, error)
	// OnConfig is optional. It is called for every non-empty config that is accepted for registration.
	OnConfig func(cfg C)
}

// Service runs the registration lifecycle for one NF.
type Service[C any] struct {
	cfg Config[C]
	log *zap.SugaredLogger

	// keepAliveTimer and keepAliveShutdown are guarded by keepAliveTimerMutex. When keepAliveShutdown
	// is set, late registration or heartbeat completions must not re-arm the keepalive timer.
	keepAliveTimer      *time.Timer
	keepAliveShutdown   bool
	keepAliveTimerMutex sync.Mutex
	// registerCtxMutex serializes registration work: initial registration, retries and recovery after
	// a failed heartbeat.
	registerCtxMutex sync.Mutex

	// activeCancel cancels the registration context of the current config. It is guarded by cancelMutex.
	activeCancel context.CancelFunc
	cancelMutex  sync.Mutex

	afterFunc func(d time.Duration, f func()) *time.Timer
}

// New validates cfg and returns a Service.
func New[C any](cfg Config[C]) (*Service[C], error) {
	switch {
	case cfg.NfName == "":
		return nil, errors.New("nrfregistration: NfName is required")
	case cfg.Register == nil:
		return nil, errors.New("nrfregistration: Register is required")
	case cfg.Update == nil:
		return nil, errors.New("nrfregistration: Update is required")
	case cfg.Deregister == nil:
		return nil, errors.New("nrfregistration: Deregister is required")
	case cfg.IsEmpty == nil:
		return nil, errors.New("nrfregistration: IsEmpty is required")
	}
	if cfg.RetryTime <= 0 {
		cfg.RetryTime = defaultRetryTime
	}
	log := cfg.Log
	if log == nil {
		log = logger.NrfRegistrationLog
	}
	return &Service[C]{cfg: cfg, log: log, afterFunc: time.AfterFunc}, nil
}

// Start runs the registration service until ctx is done or cfgChan is closed. If the new config is
// empty, the NF deregisters from the NRF. Else, it registers to the NRF. A new config cancels the
// registration in progress, so only one registration process runs at a time.
func (s *Service[C]) Start(ctx context.Context, cfgChan <-chan C) {
	var lastCfg C
	hasLastCfg := false

	s.keepAliveTimerMutex.Lock()
	s.keepAliveShutdown = false
	s.keepAliveTimerMutex.Unlock()

	s.log.Infof("started %s registration to NRF service", s.cfg.NfName)
	for {
		select {
		case <-ctx.Done():
			s.cancelRegistration()
			s.shutdownKeepAliveTimer()
			s.log.Infof("%s NF registration service shutting down", s.cfg.NfName)
			return
		case newCfg, ok := <-cfgChan:
			if !ok {
				s.cancelRegistration()
				s.shutdownKeepAliveTimer()
				s.log.Infof("%s config channel closed; stopping NF registration service", s.cfg.NfName)
				return
			}

			if s.cfg.IsEmpty(newCfg) {
				s.log.Debugf("config is empty. %s will deregister", s.cfg.NfName)
				s.deregister(ctx)
				var zero C
				lastCfg, hasLastCfg = zero, false
				continue
			}

			if hasLastCfg && s.cfg.RequiresRegistration != nil && !s.cfg.RequiresRegistration(newCfg, lastCfg) {
				s.log.Debugf("%s: no changes in NF profile relevant config. Skipping re-registration", s.cfg.NfName)
				continue
			}

			if s.cfg.Clone != nil {
				cloned, err := s.cfg.Clone(newCfg)
				if err != nil {
					s.log.Errorf("%s: failed to copy config, registration skipped: %v", s.cfg.NfName, err)
					continue
				}
				lastCfg = cloned
			} else {
				lastCfg = newCfg
			}
			hasLastCfg = true

			// Cancel current sync if running
			s.cancelRegistration()
			s.log.Debugf("config is not empty. %s will update registration", s.cfg.NfName)
			// Create new cancellable context for this registration, derived from ctx so that the
			// caller's deadline and values (e.g. the OAuth2 token source) reach the NRF requests
			registerCtx, registerCancel := context.WithCancel(ctx)
			s.setRegistrationCancel(registerCancel)
			go s.registerNF(registerCtx, newCfg)
			if s.cfg.OnConfig != nil {
				s.cfg.OnConfig(newCfg)
			}
		}
	}
}

// StartStatic is Start for an NF whose registration does not depend on dynamic configuration: it
// registers cfg once and keeps the registration alive until ctx is done. cfg must not be empty
// according to IsEmpty. Use Deregister to remove the NF from the NRF afterwards.
func (s *Service[C]) StartStatic(ctx context.Context, cfg C) {
	cfgChan := make(chan C, 1)
	cfgChan <- cfg
	s.Start(ctx, cfgChan)
}

// Deregister cancels the registration in progress, stops the heartbeat, and sends a DeregisterNFInstance. It is
// meant for NF termination and is terminal: until Start is called again, a Start loop that is still running
// registers nothing, so that a configuration received after the deregistration cannot register the NF again
// without a heartbeat to keep it registered.
//
// A registration that is in flight is waited for, and the wait ends when ctx does: give ctx a deadline, so that
// a request to the NRF that does not return cannot hold up the shutdown of the NF.
func (s *Service[C]) Deregister(ctx context.Context) {
	s.shutdownKeepAliveTimer()
	s.cancelRegistrationAndWait(ctx)
	s.sendDeregister(ctx)
}

// deregister is Deregister for an empty config: Start stays able to register a later config.
//
// The registration work is cancelled, and waited for, before the timer is stopped. The other way round, a
// registration or heartbeat that completes after the stop but before its context is cancelled would arm a
// new timer, which then stays armed after the deregistration and sends heartbeats for an NF that is gone.
// Once the context is cancelled nothing can arm a timer, so the stop is final.
func (s *Service[C]) deregister(ctx context.Context) {
	s.cancelRegistrationAndWait(ctx)
	s.keepAliveTimerMutex.Lock()
	s.stopKeepAliveTimer()
	s.keepAliveTimerMutex.Unlock()
	s.sendDeregister(ctx)
}

func (s *Service[C]) setRegistrationCancel(cancel context.CancelFunc) {
	s.cancelMutex.Lock()
	defer s.cancelMutex.Unlock()
	s.activeCancel = cancel
}

// cancelRegistration cancels the current registration context, if any.
func (s *Service[C]) cancelRegistration() {
	s.cancelMutex.Lock()
	defer s.cancelMutex.Unlock()
	if s.activeCancel != nil {
		s.log.Infoln("NF registration context cancelled")
		s.activeCancel()
		s.activeCancel = nil
	}
}

// cancelRegistrationAndWait cancels the current registration context and waits until the registration
// work running under it (including retries and heartbeat recovery) has returned, so that no Register
// request can follow the deregistration. The wait gives up when ctx is done.
func (s *Service[C]) cancelRegistrationAndWait(ctx context.Context) {
	s.cancelRegistration()
	idle := make(chan struct{})
	go func() {
		s.registerCtxMutex.Lock()
		defer s.registerCtxMutex.Unlock()
		s.log.Debugln("registration work in progress has stopped")
		close(idle)
	}()
	select {
	case <-idle:
	case <-ctx.Done():
		s.log.Warnln("timed out waiting for the registration in progress to stop")
	}
}

func (s *Service[C]) sendDeregister(ctx context.Context) {
	if err := s.cfg.Deregister(ctx); err != nil {
		s.log.Warnln("deregister instance from NRF error:", err.Error())
		return
	}
	s.log.Infoln("deregister instance from NRF successful")
}

// registerNF sends a RegisterNFInstance. If it fails, it keeps retrying, until the context is cancelled
// by Start.
func (s *Service[C]) registerNF(registerCtx context.Context, cfg C) {
	s.registerCtxMutex.Lock()
	defer s.registerCtxMutex.Unlock()
	interval := 0 * time.Millisecond
	for {
		// a timer that is stopped when the context ends, where time.After would stay until it fired
		timer := time.NewTimer(interval)
		select {
		case <-registerCtx.Done():
			timer.Stop()
			s.log.Infoln("no-op. Registration context was cancelled")
			return
		case <-timer.C:
			// select picks randomly when both cases are ready, so check cancellation first
			if registerCtx.Err() != nil {
				s.log.Infoln("no-op. Registration context was cancelled")
				return
			}
			// Checked here, with registerCtxMutex held: Deregister waits for this mutex after setting the
			// flag, so a registration is either already in flight, and waited for, or sees the flag.
			if s.isShutdown() {
				s.log.Infoln("NF registration service is shut down, the NF will not be registered")
				return
			}
			nfProfile, err := s.cfg.Register(registerCtx, cfg)
			if err != nil {
				s.log.Errorf("register %s instance to NRF failed. Will retry in %s: %v", s.cfg.NfName, s.cfg.RetryTime, err)
				interval = s.cfg.RetryTime
				continue
			}
			s.log.Infof("register %s instance to NRF with updated profile succeeded", s.cfg.NfName)
			if registerCtx.Err() != nil {
				// Superseded or deregistered while the request was in flight; do not re-arm the heartbeat
				s.log.Infoln("registration context was cancelled, heartbeat timer will not be started")
				return
			}
			s.startKeepAliveTimer(registerCtx, nfProfile.GetHeartBeatTimer(), cfg)
			return
		}
	}
}

// heartbeatNF is the callback function, this is called when keepalivetimer elapsed.
// It sends an Update NF instance to the NRF. If it fails, it registers again through registerNF, which
// shares the serialization, cancellation checks and retries of the regular registration and restarts
// the keepAliveTimer itself. Otherwise keepAliveTimer is restarted at the end, keeping the interval
// negotiated with the NRF unless the response supplies a new positive one.
func (s *Service[C]) heartbeatNF(registerCtx context.Context, cfg C, interval int32) {
	s.keepAliveTimerMutex.Lock()
	if s.keepAliveTimer == nil {
		s.keepAliveTimerMutex.Unlock()
		s.log.Infoln("heartbeat timer has been stopped, heartbeat will not be sent to NRF")
		return
	}
	s.keepAliveTimerMutex.Unlock()
	if registerCtx.Err() != nil {
		s.log.Infoln("registration was cancelled, heartbeat will not be sent to NRF")
		return
	}

	patchItem := []models.PatchItem{
		{
			Op:    models.PATCHOPERATION_REPLACE,
			Path:  "/nfStatus",
			Value: models.NFSTATUS_REGISTERED,
		},
	}
	nfProfile, problemDetails, err := s.cfg.Update(registerCtx, patchItem)

	if s.shouldRegister(problemDetails, err) {
		s.log.Debugln("NF heartbeat failed. Trying to register again")
		s.registerNF(registerCtx, cfg)
		return
	}
	s.log.Debugf("%s update NF instance (heartbeat) succeeded", s.cfg.NfName)
	if next := nfProfile.GetHeartBeatTimer(); next > 0 {
		interval = next
	}
	s.startKeepAliveTimer(registerCtx, interval, cfg)
}

func (s *Service[C]) shouldRegister(problemDetails *models.ProblemDetails, err error) bool {
	if problemDetails != nil {
		s.log.Warnf("%s update NF instance (heartbeat) problem details: %+v", s.cfg.NfName, problemDetails)
		return true
	}
	if err != nil {
		s.log.Warnf("%s update NF instance (heartbeat) error: %v", s.cfg.NfName, err)
		return true
	}
	return false
}

func (s *Service[C]) startKeepAliveTimer(registerCtx context.Context, profileHeartbeatTimer int32, cfg C) {
	s.keepAliveTimerMutex.Lock()
	defer s.keepAliveTimerMutex.Unlock()
	if s.keepAliveShutdown || registerCtx.Err() != nil {
		s.log.Infoln("NF registration was shut down or superseded, heartbeat timer will not be started")
		return
	}
	s.stopKeepAliveTimer()
	heartbeatTimer := defaultHeartbeatTimer
	if profileHeartbeatTimer > 0 {
		heartbeatTimer = profileHeartbeatTimer
	}
	heartbeatFunction := func() { s.heartbeatNF(registerCtx, cfg, heartbeatTimer) }
	// AfterFunc starts a timer and waits for keepAliveTimer to elapse and then calls heartbeatNF function
	s.keepAliveTimer = s.afterFunc(time.Duration(heartbeatTimer)*time.Second, heartbeatFunction)
	s.log.Debugf("started heartbeat timer: %d sec", heartbeatTimer)
}

// isShutdown reports whether the service was shut down, by Deregister or by the end of Start, and not
// started again.
func (s *Service[C]) isShutdown() bool {
	s.keepAliveTimerMutex.Lock()
	defer s.keepAliveTimerMutex.Unlock()
	return s.keepAliveShutdown
}

// stopKeepAliveTimer must be called with keepAliveTimerMutex held.
func (s *Service[C]) stopKeepAliveTimer() {
	if s.keepAliveTimer != nil {
		s.keepAliveTimer.Stop()
		s.keepAliveTimer = nil
		s.log.Debugln("stopped heartbeat timer")
	}
}

// shutdownKeepAliveTimer stops the keepalive timer and prevents it from being started again until
// Start is called again.
func (s *Service[C]) shutdownKeepAliveTimer() {
	s.keepAliveTimerMutex.Lock()
	defer s.keepAliveTimerMutex.Unlock()
	s.keepAliveShutdown = true
	s.stopKeepAliveTimer()
}
