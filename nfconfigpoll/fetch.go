// Copyright (C) 2026 Intel Corporation
// SPDX-License-Identifier: Apache-2.0

package nfconfigpoll

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
)

const (
	contentTypeJSON = "application/json"

	// maxResponseBytes bounds what is read of a response: a configuration is a short list, and an endpoint that
	// sends more is not one to buffer in memory.
	maxResponseBytes = 16 << 20
)

// FetchJSON makes an HTTP GET request to url and decodes the JSON response body into a T. Anything
// other than a 200 response with a JSON Content-Type is an error. The status is checked first, so that an
// error response of a proxy or a server in front of the endpoint, whatever its body is, is reported as what it is.
func FetchJSON[T any](ctx context.Context, client *http.Client, url string) (T, error) {
	var zero T

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return zero, fmt.Errorf("failed to create HTTP request: %w", err)
	}
	req.Header.Set("Accept", contentTypeJSON)

	resp, err := client.Do(req)
	if err != nil {
		return zero, fmt.Errorf("HTTP GET %v failed: %w", url, err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusBadRequest, http.StatusInternalServerError:
		return zero, fmt.Errorf("server returned %d error code", resp.StatusCode)
	default:
		return zero, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	contentType := resp.Header.Get("Content-Type")
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil || !strings.EqualFold(mediaType, contentTypeJSON) {
		return zero, fmt.Errorf("unexpected Content-Type: got %q, want %s", contentType, contentTypeJSON)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return zero, fmt.Errorf("failed to read response body: %w", err)
	}
	if len(body) > maxResponseBytes {
		return zero, fmt.Errorf("response body is larger than %d bytes", maxResponseBytes)
	}
	var config T
	if err := json.Unmarshal(body, &config); err != nil {
		return zero, fmt.Errorf("failed to parse JSON response: %w", err)
	}
	return config, nil
}
