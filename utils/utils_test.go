// Copyright (C) 2026 Intel Corporation
// SPDX-License-Identifier: Apache-2.0

package utils

import (
	"errors"
	"net/http"
	"testing"

	openapi "github.com/omec-project/openapi/v2"
	"github.com/omec-project/openapi/v2/models"
)

func TestProblemDetailsFromOpenAPIErrorHandlesTransportError(t *testing.T) {
	problemDetails := ProblemDetailsFromOpenAPIError(nil, errors.New("EOF"))

	if problemDetails.GetStatus() != http.StatusInternalServerError {
		t.Fatalf("expected status %d, got %d", http.StatusInternalServerError, problemDetails.GetStatus())
	}
	if problemDetails.GetCause() != "SYSTEM_FAILURE" {
		t.Fatalf("expected cause SYSTEM_FAILURE, got %q", problemDetails.GetCause())
	}
	if problemDetails.GetDetail() != "EOF" {
		t.Fatalf("expected detail EOF, got %q", problemDetails.GetDetail())
	}
}

func TestProblemDetailsFromOpenAPIErrorUsesResponseStatusForHTTPError(t *testing.T) {
	problemDetails := ProblemDetailsFromOpenAPIError(
		&http.Response{StatusCode: http.StatusBadGateway, Status: "502 Bad Gateway"},
		errors.New("EOF"),
	)

	if problemDetails.GetStatus() != http.StatusBadGateway {
		t.Fatalf("expected status %d, got %d", http.StatusBadGateway, problemDetails.GetStatus())
	}
	if problemDetails.GetCause() != "SYSTEM_FAILURE" {
		t.Fatalf("expected cause SYSTEM_FAILURE, got %q", problemDetails.GetCause())
	}
	if problemDetails.GetDetail() != "EOF" {
		t.Fatalf("expected detail EOF, got %q", problemDetails.GetDetail())
	}
}

func TestProblemDetailsFromOpenAPIErrorPreservesStructuredProblemDetails(t *testing.T) {
	title := "Data not found"
	detail := "subscription was not found"
	cause := "DATA_NOT_FOUND"
	status := int32(http.StatusNotFound)
	rawModel := models.NewProblemDetails()
	rawModel.SetTitle(title)
	rawModel.SetDetail(detail)
	rawModel.SetCause(cause)
	rawModel.SetStatus(status)

	problemDetails := ProblemDetailsFromOpenAPIError(
		&http.Response{StatusCode: http.StatusNotFound, Status: "404 Not Found"},
		&openapi.GenericOpenAPIError{
			RawError: openapi.FormatErrorMessage("404 Not Found", rawModel),
			RawModel: rawModel,
		},
	)

	if problemDetails.GetTitle() != title {
		t.Fatalf("expected title %q, got %q", title, problemDetails.GetTitle())
	}
	if problemDetails.GetDetail() != detail {
		t.Fatalf("expected detail %q, got %q", detail, problemDetails.GetDetail())
	}
	if problemDetails.GetCause() != cause {
		t.Fatalf("expected cause %q, got %q", cause, problemDetails.GetCause())
	}
	if problemDetails.GetStatus() != http.StatusNotFound {
		t.Fatalf("expected status %d, got %d", http.StatusNotFound, problemDetails.GetStatus())
	}
}
