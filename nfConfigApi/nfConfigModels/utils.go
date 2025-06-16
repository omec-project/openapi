// SPDX-FileCopyrightText: 2025 Canonical Ltd
//
// SPDX-License-Identifier: Apache-2.0
//

package nfConfigModels

import "reflect"

func IsNil(i interface{}) bool {
	return i == nil || (reflect.TypeOf(i).Kind() == reflect.Ptr && reflect.ValueOf(i).IsNil())
}
