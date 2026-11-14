// Copyright (C) 2026 Intel Corporation
// SPDX-License-Identifier: Apache-2.0

package nrfclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/omec-project/openapi/v2"
	"github.com/omec-project/openapi/v2/models"
)

var errNoResponse = errors.New("no response from server")

// requireID rejects an empty identifier: it would leave a path parameter empty, and the request would
// address the collection ("/nf-instances/") instead of the resource.
func requireID(name, value string) error {
	if value == "" {
		return fmt.Errorf("%s is required", name)
	}
	return nil
}

// RegisterResult is the outcome of a successful Register.
type RegisterResult struct {
	// Profile is the profile the NRF returned. It may be nil.
	Profile *models.NFProfile
	// Created is true if the NRF created the registration (201), false if it replaced an existing
	// one (200).
	Created bool
	// NrfURI and NfInstanceID are taken from the Location header of a 201 response. They are empty
	// if the response had no usable Location.
	NrfURI       string
	NfInstanceID string
}

// Register sends a RegisterNFInstance for profile. The NF instance ID in the profile is used in the
// request path.
func (c *Client) Register(ctx context.Context, profile models.NFProfile) (*RegisterResult, error) {
	if err := requireID("NF instance ID of the profile", profile.GetNfInstanceId()); err != nil {
		return nil, err
	}
	client := c.management()
	c.log.Debugf("registering NF instance using profile: %+v", profile)
	req := client.NFInstanceIDDocumentAPI.RegisterNFInstance(ctx, profile.GetNfInstanceId()).NFProfile(profile)
	received, res, err := client.NFInstanceIDDocumentAPI.RegisterNFInstanceExecute(req)
	defer c.done(OpRegisterNFInstance, res, err)
	if err != nil {
		return nil, err
	}
	if res == nil {
		return nil, errNoResponse
	}

	switch res.StatusCode {
	case http.StatusOK: // NFUpdate
		return &RegisterResult{Profile: received}, nil
	case http.StatusCreated: // NFRegister
		result := &RegisterResult{Profile: received, Created: true}
		location := res.Header.Get("Location")
		result.NrfURI, result.NfInstanceID = parseLocation(location)
		if result.NfInstanceID == "" {
			c.log.Warnf("NRF created the registration but returned no usable Location header: %q", location)
		}
		return result, nil
	default:
		return nil, fmt.Errorf("NRF returned unexpected status code %d", res.StatusCode)
	}
}

// parseLocation splits "<nrfURI>/nnrf-nfm/v1/nf-instances/<id>" into the NRF URI and the instance ID.
// The instance ID is the last path segment and is returned whenever there is one. The NRF URI is only
// returned if location contains the "/nnrf-nfm/" path.
func parseLocation(location string) (nrfURI, nfInstanceID string) {
	nfInstanceID = location[strings.LastIndex(location, "/")+1:]
	if i := strings.Index(location, "/nnrf-nfm/"); i >= 0 {
		nrfURI = location[:i]
	}
	return nrfURI, nfInstanceID
}

// Update sends an UpdateNFInstance with patch. If the NRF answers with a ProblemDetails it is
// returned together with the error. The returned profile may be nil, e.g. on 204.
func (c *Client) Update(ctx context.Context, nfInstanceID string, patch []models.PatchItem) (*models.NFProfile, *models.ProblemDetails, error) {
	if err := requireID("NF instance ID", nfInstanceID); err != nil {
		return nil, nil, err
	}
	client := c.management()
	req := client.NFInstanceIDDocumentAPI.UpdateNFInstance(ctx, nfInstanceID).PatchItem(patch)
	received, res, err := client.NFInstanceIDDocumentAPI.UpdateNFInstanceExecute(req)
	defer c.done(OpUpdateNFInstance, res, err)
	if err != nil {
		if res != nil {
			c.log.Warnf("UpdateNFInstance received error response: %v", res.Status)
		}
		if problem, ok := openapi.ErrorModel[models.ProblemDetails](err); ok {
			return nil, &problem, err
		}
		return nil, nil, err
	}
	if res == nil {
		return nil, nil, errNoResponse
	}
	if res.StatusCode == http.StatusOK || res.StatusCode == http.StatusNoContent {
		return received, nil, nil
	}
	return nil, nil, fmt.Errorf("unexpected response code %d", res.StatusCode)
}

// Deregister sends a DeregisterNFInstance. Only 204 is success.
func (c *Client) Deregister(ctx context.Context, nfInstanceID string) error {
	if err := requireID("NF instance ID", nfInstanceID); err != nil {
		return err
	}
	client := c.management()
	req := client.NFInstanceIDDocumentAPI.DeregisterNFInstance(ctx, nfInstanceID)
	res, err := client.NFInstanceIDDocumentAPI.DeregisterNFInstanceExecute(req)
	defer c.done(OpDeregisterNFInstance, res, err)
	if err != nil {
		return err
	}
	if res == nil {
		return errNoResponse
	}
	if res.StatusCode != http.StatusNoContent {
		return fmt.Errorf("unexpected response code %d", res.StatusCode)
	}
	return nil
}

// CreateSubscription creates an NF status subscription. The NRF answers 201 with the data of the created
// subscription (TS 29.510, clause 5.2.2.5.2); anything else is an error, since the generated client treats every
// status below 300 as success and decodes an empty body without complaint. If the NRF answers with a
// ProblemDetails it is returned together with the error.
func (c *Client) CreateSubscription(ctx context.Context, data models.SubscriptionData) (*models.SubscriptionData, *models.ProblemDetails, error) {
	client := c.management()
	req := client.SubscriptionsCollectionAPI.CreateSubscription(ctx).SubscriptionData(data)
	created, res, err := client.SubscriptionsCollectionAPI.CreateSubscriptionExecute(req)
	defer c.done(OpCreateSubscription, res, err)
	if err == nil {
		if res == nil {
			return nil, nil, errNoResponse
		}
		if res.StatusCode != http.StatusCreated {
			return nil, nil, fmt.Errorf("unexpected response code %d, want %d", res.StatusCode, http.StatusCreated)
		}
		if created == nil {
			return nil, nil, errors.New("NRF answered 201 without the data of the subscription")
		}
		return created, nil, nil
	}
	if res == nil {
		return nil, nil, fmt.Errorf("%w: %w", errNoResponse, err)
	}
	c.log.Errorf("CreateSubscription received error response: %v", res.Status)
	if problem, ok := openapi.ErrorModel[models.ProblemDetails](err); ok {
		return nil, &problem, err
	}
	return nil, nil, err
}

// RemoveSubscription removes an NF status subscription. The NRF answers 204 (TS 29.510, clause 5.2.2.5.2 and
// the Nnrf_NFManagement API); any other success status, such as 202, does not confirm the removal and is an
// error. If the NRF answers with a ProblemDetails it is returned together with the error.
func (c *Client) RemoveSubscription(ctx context.Context, subscriptionID string) (*models.ProblemDetails, error) {
	if err := requireID("subscription ID", subscriptionID); err != nil {
		return nil, err
	}
	client := c.management()
	req := client.SubscriptionIDDocumentAPI.RemoveSubscription(ctx, subscriptionID)
	res, err := client.SubscriptionIDDocumentAPI.RemoveSubscriptionExecute(req)
	defer c.done(OpRemoveSubscription, res, err)
	if err == nil {
		if res == nil {
			return nil, errNoResponse
		}
		if res.StatusCode != http.StatusNoContent {
			return nil, fmt.Errorf("unexpected response code %d, want %d", res.StatusCode, http.StatusNoContent)
		}
		return nil, nil
	}
	if res == nil {
		return nil, fmt.Errorf("%w: %w", errNoResponse, err)
	}
	if problem, ok := openapi.ErrorModel[models.ProblemDetails](err); ok {
		return &problem, err
	}
	return nil, err
}
