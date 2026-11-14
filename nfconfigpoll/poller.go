// Copyright (C) 2026 Intel Corporation
// SPDX-FileCopyrightText: 2025 Canonical Ltd
// SPDX-License-Identifier: Apache-2.0

// Package nfconfigpoll implements the polling of NF configuration from the webconsole shared by the
// network functions: periodic HTTP GET with exponential backoff on errors, and a callback when the
// polled configuration changed.
package nfconfigpoll

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"time"

	"github.com/omec-project/openapi/v2/logger"
	"go.uber.org/zap"
)

const (
	defaultInterval   = 5 * time.Second
	defaultMaxBackoff = 40 * time.Second
	backoffFactor     = 2
)

// Config describes what to poll and what to do with it. T is the type of the polled configuration,
// typically a slice (e.g. []models.PlmnId).
type Config[T any] struct {
	// Name is used in log messages, e.g. "PLMN". Required.
	Name string
	// Endpoint is the full URL to poll, e.g. webuiUri + "/nfconfig/plmn". Required.
	Endpoint string
	// Log is optional. It defaults to logger.NfConfigPollLog.
	Log *zap.SugaredLogger

	// Interval between polls. Defaults to 5 seconds.
	Interval time.Duration
	// MaxBackoff caps the interval after consecutive errors. Defaults to 40 seconds, and is
	// raised to Interval if smaller.
	MaxBackoff time.Duration
	// FetchTimeout bounds one request. Defaults to Interval.
	FetchTimeout time.Duration
	// Client is optional. It defaults to an http.Client with FetchTimeout.
	Client *http.Client

	// Fetch is optional. It defaults to FetchJSON[T]. Tests can inject a fake.
	Fetch func(ctx context.Context, url string) (T, error)
	// Normalize is optional. It is applied to every fetched config before it is compared and
	// delivered, so that the comparison reflects the canonical form of the config, e.g. a JSON
	// round-trip through types with custom (Un)MarshalJSON. If it fails the poll is skipped.
	Normalize func(cfg T) (T, error)
	// Initial is what the poller considers the current config before the first successful poll. The
	// first poll only calls OnChange if it differs from Initial (by reflect.DeepEqual). A nil slice
	// and an empty slice are different, so set this to what the NF used to start with, typically an
	// empty non-nil slice.
	Initial T

	// OnChange is called with the new config when it differs from the current one. Required. If it
	// returns an error the new config is NOT recorded as current, so it is delivered again on the
	// next poll. OnChange runs on the polling goroutine; blocking operations should honor ctx.
	OnChange func(ctx context.Context, cfg T) error
}

// Poller runs the polling loop for one configuration endpoint.
type Poller[T any] struct {
	cfg     Config[T]
	log     *zap.SugaredLogger
	current T
}

// New validates cfg, fills in defaults and returns a Poller.
func New[T any](cfg Config[T]) (*Poller[T], error) {
	switch {
	case cfg.Name == "":
		return nil, errors.New("nfconfigpoll: Name is required")
	case cfg.Endpoint == "":
		return nil, errors.New("nfconfigpoll: Endpoint is required")
	case cfg.OnChange == nil:
		return nil, errors.New("nfconfigpoll: OnChange is required")
	}
	if cfg.Interval <= 0 {
		cfg.Interval = defaultInterval
	}
	if cfg.MaxBackoff <= 0 {
		cfg.MaxBackoff = defaultMaxBackoff
	}
	// a cap below the polling interval would make the poller poll faster while the endpoint is failing
	if cfg.MaxBackoff < cfg.Interval {
		cfg.MaxBackoff = cfg.Interval
	}
	if cfg.FetchTimeout <= 0 {
		cfg.FetchTimeout = cfg.Interval
	}
	if cfg.Client == nil {
		cfg.Client = &http.Client{Timeout: cfg.FetchTimeout}
	}
	if cfg.Fetch == nil {
		client := cfg.Client
		cfg.Fetch = func(ctx context.Context, url string) (T, error) {
			return FetchJSON[T](ctx, client, url)
		}
	}
	log := cfg.Log
	if log == nil {
		log = logger.NfConfigPollLog
	}
	return &Poller[T]{cfg: cfg, log: log, current: cfg.Initial}, nil
}

// Run polls until ctx is done. The first poll happens after one interval.
func (p *Poller[T]) Run(ctx context.Context) {
	interval := p.cfg.Interval
	p.log.Infof("started polling service on %s every %v", p.cfg.Endpoint, p.cfg.Interval)
	for {
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			p.log.Infoln("polling service shutting down")
			return
		case <-timer.C:
		}

		if err := p.poll(ctx); err != nil {
			if ctx.Err() != nil {
				p.log.Infoln("polling service shutting down")
				return
			}
			interval = min(interval*backoffFactor, p.cfg.MaxBackoff)
			p.log.Errorf("polling error. Retrying in %v: %+v", interval, err)
			continue
		}
		interval = p.cfg.Interval
	}
}

// poll fetches once and delivers the config if it changed. An error means the fetch failed and the
// poller should back off. Normalize and OnChange failures are logged and retried at the normal
// interval, as the endpoint itself is healthy.
func (p *Poller[T]) poll(ctx context.Context) error {
	fetchCtx, cancel := context.WithTimeout(ctx, p.cfg.FetchTimeout)
	defer cancel()
	newCfg, err := p.cfg.Fetch(fetchCtx, p.cfg.Endpoint)
	if err != nil {
		return err
	}

	if p.cfg.Normalize != nil {
		newCfg, err = p.cfg.Normalize(newCfg)
		if err != nil {
			p.log.Errorf("failed to normalize %s config: %v", p.cfg.Name, err)
			return nil
		}
	}

	if reflect.DeepEqual(p.current, newCfg) {
		p.log.Debugf("%s config did not change %+v", p.cfg.Name, newCfg)
		return nil
	}
	p.log.Infof("%s config changed. New %s config: %+v", p.cfg.Name, p.cfg.Name, newCfg)
	if err := p.cfg.OnChange(ctx, newCfg); err != nil {
		p.log.Errorf("failed to apply %s config, will retry on next poll: %v", p.cfg.Name, err)
		return nil
	}
	p.current = newCfg
	return nil
}

// Send delivers v on ch, or returns ctx.Err() if ctx is done first. It is meant for OnChange
// handlers that forward the config over a channel.
func Send[T any](ctx context.Context, ch chan<- T, v T) error {
	select {
	case ch <- v:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// JSONRoundTrip is a Normalize function that marshals and unmarshals the config, so that the stored
// and compared value reflects the custom (Un)MarshalJSON behavior of the generated models.
func JSONRoundTrip[T any](cfg T) (T, error) {
	var out T
	data, err := json.Marshal(cfg)
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return out, err
	}
	return out, nil
}
