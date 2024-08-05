// Copyright 2019 Communication Service/Software Laboratory, National Chiao Tung University (free5gc.org)
//
// SPDX-License-Identifier: Apache-2.0

package openapi

import (
	"encoding/json"
	"reflect"
)

func MarshToJsonString(v interface{}) (result []string) {
	types := reflect.TypeOf(v)
	val := reflect.ValueOf(v)
	if types.Kind() == reflect.Slice {
		for i := 0; i < val.Len(); i++ {
			tmp, _ := json.Marshal(val.Index(i).Interface())
			result = append(result, string(tmp))
		}
	} else {
		tmp, _ := json.Marshal(v)
		result = append(result, string(tmp))
	}
	return
}
