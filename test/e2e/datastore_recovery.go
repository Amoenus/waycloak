// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package e2e

import (
	"errors"
	"fmt"
	"strings"
)

type datastoreRecoveryConfig struct {
	SnapshotCommand string
	RestoreCommand  string
}

func parseDatastoreRecoveryConfig(snapshotCommand, restoreCommand string) (datastoreRecoveryConfig, error) {
	snapshotCommand = strings.TrimSpace(snapshotCommand)
	restoreCommand = strings.TrimSpace(restoreCommand)
	if (snapshotCommand == "") != (restoreCommand == "") {
		return datastoreRecoveryConfig{}, errors.New("datastore recovery requires both snapshot and restore commands")
	}
	return datastoreRecoveryConfig{SnapshotCommand: snapshotCommand, RestoreCommand: restoreCommand}, nil
}

type restoredIdentities struct {
	NamespaceUID string
	PodUID       string
	BindingUID   string
}

func validateRestoredIdentities(expected, actual restoredIdentities, markerPresent bool) error {
	if markerPresent {
		return errors.New("post-snapshot marker survived datastore restore")
	}
	for _, identity := range []struct {
		name     string
		expected string
		actual   string
	}{
		{name: "Namespace", expected: expected.NamespaceUID, actual: actual.NamespaceUID},
		{name: "Pod", expected: expected.PodUID, actual: actual.PodUID},
		{name: "VPNWorkloadBinding", expected: expected.BindingUID, actual: actual.BindingUID},
	} {
		if identity.actual == "" {
			return fmt.Errorf("restored %s identity is missing", identity.name)
		}
		if identity.actual != identity.expected {
			return fmt.Errorf("restored %s UID drifted: expected %q, got %q", identity.name, identity.expected, identity.actual)
		}
	}
	return nil
}
