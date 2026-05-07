// Copyright (C) 2026 Intel Corporation
// SPDX-License-Identifier: Apache-2.0

package utils

import (
	"net/http"

	"github.com/omec-project/openapi/v2/models"
)

func ProblemDetailsSystemFailure(detail string) *models.ProblemDetails {
	problemDetails := models.NewProblemDetails()
	problemDetails.SetTitle("System failure")
	problemDetails.SetStatus(http.StatusInternalServerError)
	problemDetails.SetCause("SYSTEM_FAILURE")
	problemDetails.SetDetail(detail)
	return problemDetails
}

func ProblemDetailsMalformedRequestSyntax(detail string) *models.ProblemDetails {
	problemDetails := models.NewProblemDetails()
	problemDetails.SetTitle("Malformed request syntax")
	problemDetails.SetStatus(http.StatusBadRequest)
	problemDetails.SetDetail(detail)
	return problemDetails
}

func ProblemDetailsUnspecified() *models.ProblemDetails {
	problemDetails := models.NewProblemDetails()
	problemDetails.SetTitle("Unspecified")
	problemDetails.SetStatus(http.StatusForbidden)
	problemDetails.SetCause("UNSPECIFIED")
	return problemDetails
}
