// SPDX-License-Identifier: Apache-2.0
// Copyright 2022 Authors of Cilium

//go:build !privileged_tests
// +build !privileged_tests

package kvstoremesh

import (
	"strings"
	"testing"
)

func TestKVStoreMeshKeys(t *testing.T) {
	for keyName, keyInfo := range AlienKeys {
		t.Run(keyName, func(t *testing.T) {
			key := keyInfo.ExampleStr
			num := keyInfo.NumFields

			items := strings.Split(key, "/")
			if len(items) != keyInfo.NumFields {
				t.Errorf("Key & NumFields mismatch: key %s, %d, %d", key, len(items), num)
			}
		})
	}
}
