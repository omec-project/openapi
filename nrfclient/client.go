// Copyright (C) 2026 Intel Corporation
// SPDX-License-Identifier: Apache-2.0

// Package nrfclient is the client the network functions use to talk to the NRF for NF management:
// registering, updating and deregistering the NF instance, and managing NF status subscriptions.
// It wraps the generated Nnrf_NFManagement client and replaces the per-NF copies of that code.
package nrfclient

import (
	"net/http"

	"github.com/omec-project/openapi/v2/Nnrf_NFDiscovery"
	"github.com/omec-project/openapi/v2/Nnrf_NFManagement"
	"github.com/omec-project/openapi/v2/logger"
	"go.uber.org/zap"
)

// Operation names the NRF operation an Observer is told about.
type Operation string

const (
	OpRegisterNFInstance   Operation = "RegisterNFInstance"
	OpUpdateNFInstance     Operation = "UpdateNFInstance"
	OpDeregisterNFInstance Operation = "DeregisterNFInstance"
	OpCreateSubscription   Operation = "CreateSubscription"
	OpRemoveSubscription   Operation = "RemoveSubscription"
)

// Observer is called after every request to the NRF, with the raw response (nil if there was none)
// and the error from the generated client. It exists for NF-specific metrics and tracing and must
// not read or close res.Body.
type Observer func(op Operation, res *http.Response, err error)

// Client talks to one NRF.
type Client struct {
	nrfURI     string
	httpClient *http.Client
	observer   Observer
	log        *zap.SugaredLogger
}

// Option customizes a Client.
type Option func(*Client)

// WithHTTPClient sets the HTTP client used for NF management requests, e.g. to configure TLS.
func WithHTTPClient(c *http.Client) Option { return func(cl *Client) { cl.httpClient = c } }

// WithObserver sets an Observer.
func WithObserver(o Observer) Option { return func(cl *Client) { cl.observer = o } }

// WithLogger sets the logger. It defaults to logger.NrfClientLog, which a nil logger also selects.
func WithLogger(l *zap.SugaredLogger) Option { return func(cl *Client) { cl.log = l } }

// New returns a Client for the NRF at nrfURI (the apiRoot, e.g. "http://nrf:8000").
func New(nrfURI string, opts ...Option) *Client {
	c := &Client{nrfURI: nrfURI, log: logger.NrfClientLog}
	for _, opt := range opts {
		opt(c)
	}
	if c.log == nil { // WithLogger(nil)
		c.log = logger.NrfClientLog
	}
	return c
}

// NewNFManagementClient returns the generated Nnrf_NFManagement client pointed at nrfURI. httpClient
// may be nil.
func NewNFManagementClient(nrfURI string, httpClient *http.Client) *Nnrf_NFManagement.APIClient {
	cfg := Nnrf_NFManagement.NewConfiguration()
	serverConfig := &cfg.Servers[0]
	if apiRootVar, exists := serverConfig.Variables["apiRoot"]; exists {
		apiRootVar.DefaultValue = nrfURI
		serverConfig.Variables["apiRoot"] = apiRootVar
	}
	if httpClient != nil {
		cfg.HTTPClient = httpClient
	}
	return Nnrf_NFManagement.NewAPIClient(cfg)
}

// NewNFDiscoveryClient returns the generated Nnrf_NFDiscovery client pointed at nrfURI.
func NewNFDiscoveryClient(nrfURI string) *Nnrf_NFDiscovery.APIClient {
	cfg := Nnrf_NFDiscovery.NewConfiguration()
	serverConfig := &cfg.Servers[0]
	if apiRootVar, exists := serverConfig.Variables["apiRoot"]; exists {
		apiRootVar.DefaultValue = nrfURI
		serverConfig.Variables["apiRoot"] = apiRootVar
	}
	return Nnrf_NFDiscovery.NewAPIClient(cfg)
}

func (c *Client) management() *Nnrf_NFManagement.APIClient {
	return NewNFManagementClient(c.nrfURI, c.httpClient)
}

// done reports the outcome to the observer and closes the response body.
func (c *Client) done(op Operation, res *http.Response, err error) {
	if c.observer != nil {
		c.observer(op, res, err)
	}
	if res == nil || res.Body == nil {
		return
	}
	if closeErr := res.Body.Close(); closeErr != nil {
		c.log.Errorf("%s response body cannot close: %+v", op, closeErr)
	}
}
