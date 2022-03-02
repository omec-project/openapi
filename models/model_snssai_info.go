// Copyright 2019 Communication Service/Software Laboratory, National Chiao Tung University (free5gc.org)
//
// SPDX-License-Identifier: Apache-2.0

/*
 * Nudm_SDM
 *
 * Nudm Subscriber Data Management Service
 *
 * API version: 2.0.0
 * Generated by: OpenAPI Generator (https://openapi-generator.tech)
 */

package models

type SnssaiInfo struct {
	DnnInfos []DnnInfo `json:"dnnInfos" yaml:"dnnInfos" bson:"dnnInfos" mapstructure:"DnnInfos"`
}
