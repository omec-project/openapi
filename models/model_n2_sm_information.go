// Copyright 2019 Communication Service/Software Laboratory, National Chiao Tung University (free5gc.org)
//
// SPDX-License-Identifier: Apache-2.0

/*
 * Namf_Communication
 *
 * AMF Communication Service
 *
 * API version: 1.0.0
 * Generated by: OpenAPI Generator (https://openapi-generator.tech)
 */

package models

type N2SmInformation struct {
	PduSessionId  int32          `json:"pduSessionId"`
	N2InfoContent *N2InfoContent `json:"n2InfoContent,omitempty"`
	SNssai        *Snssai        `json:"sNssai,omitempty"`
	SubjectToHo   bool           `json:"subjectToHo,omitempty"`
}
