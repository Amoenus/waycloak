// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package e2e

import (
	"strings"
	"testing"
)

func TestParseDatastoreRecoveryConfig(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		snapshot string
		restore  string
		wantErr  bool
	}{
		{name: "disabled"},
		{name: "paired", snapshot: "snapshot", restore: "restore"},
		{name: "snapshot only", snapshot: "snapshot", wantErr: true},
		{name: "restore only", restore: "restore", wantErr: true},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			config, err := parseDatastoreRecoveryConfig(test.snapshot, test.restore)
			if (err != nil) != test.wantErr {
				t.Fatalf("parseDatastoreRecoveryConfig() error = %v, wantErr %t", err, test.wantErr)
			}
			if err == nil && (config.SnapshotCommand != strings.TrimSpace(test.snapshot) || config.RestoreCommand != strings.TrimSpace(test.restore)) {
				t.Fatalf("parseDatastoreRecoveryConfig() = %#v", config)
			}
		})
	}
}

func TestValidateRestoredIdentities(t *testing.T) {
	t.Parallel()
	expected := restoredIdentities{NamespaceUID: "namespace", PodUID: "pod", BindingUID: "binding"}
	for _, test := range []struct {
		name    string
		actual  restoredIdentities
		marker  bool
		wantErr string
	}{
		{name: "coherent", actual: expected},
		{name: "marker", actual: expected, marker: true, wantErr: "marker survived"},
		{name: "namespace missing", actual: restoredIdentities{PodUID: "pod", BindingUID: "binding"}, wantErr: "Namespace identity is missing"},
		{name: "pod drift", actual: restoredIdentities{NamespaceUID: "namespace", PodUID: "different", BindingUID: "binding"}, wantErr: "Pod UID drifted"},
		{name: "binding missing", actual: restoredIdentities{NamespaceUID: "namespace", PodUID: "pod"}, wantErr: "VPNWorkloadBinding identity is missing"},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := validateRestoredIdentities(expected, test.actual, test.marker)
			if test.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("validateRestoredIdentities() error = %v, want %q", err, test.wantErr)
			}
		})
	}
}
