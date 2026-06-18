// Copyright (C) 2026 Intel Corporation
// SPDX-License-Identifier: Apache-2.0

package utils

import (
	"net/http"

	openapi "github.com/omec-project/openapi/v2"
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

func ProblemDetailsDataNotFound() *models.ProblemDetails {
	problemDetails := models.NewProblemDetails()
	problemDetails.SetStatus(http.StatusNotFound)
	problemDetails.SetCause("DATA_NOT_FOUND")
	return problemDetails
}

func ProblemDetailsUserNotFound() *models.ProblemDetails {
	problemDetails := models.NewProblemDetails()
	problemDetails.SetStatus(http.StatusNotFound)
	problemDetails.SetCause("USER_NOT_FOUND")
	return problemDetails
}

func ProblemDetailsFromOpenAPIError(res *http.Response, err error) *models.ProblemDetails {
	if err == nil {
		return nil
	}

	problemDetails := ProblemDetailsSystemFailure(err.Error())
	if res == nil || err.Error() != res.Status {
		return problemDetails
	}

	problemDetails.SetStatus(int32(res.StatusCode))
	if openapiErr, ok := err.(openapi.GenericOpenAPIError); ok {
		if details, ok := openapiErr.Model().(models.ProblemDetails); ok {
			if details.Cause != nil {
				problemDetails.SetCause(details.GetCause())
			}
			if details.Status != nil {
				problemDetails.SetStatus(details.GetStatus())
			}
		}
	}

	return problemDetails
}
