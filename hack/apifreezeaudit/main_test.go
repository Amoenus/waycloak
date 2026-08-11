// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package main

import (
	"os"
	"strings"
	"testing"
)

func TestRepositoryContract(t *testing.T) {
	data, err := os.ReadFile("../../docs/api/replacement-api-freeze.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := audit(data); err != nil {
		t.Fatal(err)
	}
}

func TestRejectsForbiddenAlphaSurface(t *testing.T) {
	data := repositoryContract(t)
	forbiddenVersion := alphaForbiddenSurfaces()[0] + "1"
	data = append(data[:len(data)-2], []byte(",\n  \"old\": \""+forbiddenVersion+"\"\n}\n")...)
	if err := audit(data); err == nil || !strings.Contains(err.Error(), "forbidden alpha surface") {
		t.Fatalf("audit() error = %v, want forbidden alpha failure", err)
	}
}

func TestRejectsBaselineReferenceGrantDependency(t *testing.T) {
	data := strings.Replace(string(repositoryContract(t)), `"referenceGrantBaseline": false`, `"referenceGrantBaseline": true`, 1)
	if err := audit([]byte(data)); err == nil || !strings.Contains(err.Error(), "ReferenceGrant baseline boundary") {
		t.Fatalf("audit() error = %v, want ReferenceGrant boundary failure", err)
	}
}

func TestRejectsAmbiguousRouteParents(t *testing.T) {
	data := strings.Replace(string(repositoryContract(t)), `"path": "spec.parentRefs", "type": "map", "keys": ["group", "kind", "namespace", "name"], "minItems": 1, "maxItems": 1`, `"path": "spec.parentRefs", "type": "map", "keys": ["group", "kind", "namespace", "name"], "minItems": 1, "maxItems": 2`, 1)
	if err := audit([]byte(data)); err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("audit() error = %v, want parent ambiguity failure", err)
	}
}

func TestRejectsObjectKeyedRouteParentStatus(t *testing.T) {
	data := strings.Replace(string(repositoryContract(t)), `"path": "status.parents", "type": "atomic", "keys": []`, `"path": "status.parents", "type": "map", "keys": ["parentRef", "controllerName"]`, 1)
	if err := audit([]byte(data)); err == nil || !strings.Contains(err.Error(), "status.parents must be") {
		t.Fatalf("audit() error = %v, want atomic parent status failure", err)
	}
}

func TestRejectsIncompleteBaselineFeatureSet(t *testing.T) {
	data := strings.Replace(string(repositoryContract(t)), `      "networking.waycloak.io/NodeRestartRecovery"`, `      "networking.waycloak.io/UnexpectedFeature"`, 1)
	if err := audit([]byte(data)); err == nil || !strings.Contains(err.Error(), "baseline features") {
		t.Fatalf("audit() error = %v, want baseline feature failure", err)
	}
}

func TestRejectsKindMissingCommonCondition(t *testing.T) {
	data := strings.Replace(string(repositoryContract(t)), `"conditions": ["Accepted", "ResolvedRefs", "Programmed", "Ready", "TunnelReady", "DNSReady", "MembershipApplied"]`, `"conditions": ["Accepted", "ResolvedRefs", "Programmed", "TunnelReady", "DNSReady", "MembershipApplied"]`, 1)
	if err := audit([]byte(data)); err == nil || !strings.Contains(err.Error(), "frozen condition contract") {
		t.Fatalf("audit() error = %v, want missing common condition failure", err)
	}
}

func TestRejectsConditionWithoutUnavailableObservation(t *testing.T) {
	data := strings.Replace(string(repositoryContract(t)), `"TunnelReady": ["TunnelReady", "TunnelNotReady", "ObservationUnavailable"]`, `"TunnelReady": ["TunnelReady", "TunnelNotReady"]`, 1)
	if err := audit([]byte(data)); err == nil || !strings.Contains(err.Error(), "unavailable observation") {
		t.Fatalf("audit() error = %v, want unavailable observation failure", err)
	}
}

func TestRejectsCrossNamespaceOwnerReference(t *testing.T) {
	data := strings.Replace(string(repositoryContract(t)), `"crossNamespace": false`, `"crossNamespace": true`, 1)
	if err := audit([]byte(data)); err == nil || !strings.Contains(err.Error(), "unsafe owner reference") {
		t.Fatalf("audit() error = %v, want owner reference failure", err)
	}
}

func repositoryContract(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("../../docs/api/replacement-api-freeze.json")
	if err != nil {
		t.Fatal(err)
	}
	return data
}
