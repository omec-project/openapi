// Copyright 2019 Communication Service/Software Laboratory, National Chiao Tung University (free5gc.org)
//
// SPDX-License-Identifier: Apache-2.0

/*
 * Namf_Communication
 *
 * AMF Communication Service
 *
 * API version: 1.0.0
 * Manually Created
 */

package models

type N1MessageNotify struct {
	JsonData            *N1MessageNotification `json:"jsonData,omitempty" multipart:"contentType:application/json"`
	BinaryDataN1Message []byte                 `json:"binaryDataN1Message,omitempty" multipart:"contentType:application/vnd.3gpp.5gnas,ref:JsonData.N1MessageContainer.N1MessageContent.ContentId"`
}
