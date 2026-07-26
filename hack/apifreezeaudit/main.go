// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

// Command apifreezeaudit verifies the implementation boundary accepted by the
// replacement API review without generating CRDs or Go API types.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"slices"
	"strings"
)

const defaultContract = "docs/api/replacement-api-freeze.json"

var requiredKinds = map[string]kindExpectation{
	"VPNGatewayClass":    {scope: "Cluster", specOwner: "Distribution"},
	"VPNGateway":         {scope: "Namespaced", specOwner: "ClusterNetworkOperator"},
	"VPNEgressRoute":     {scope: "Namespaced", specOwner: "WorkloadOwner"},
	"VPNWorkloadBinding": {scope: "Namespaced", specOwner: "ControllerOnly"},
	"PortForwardLease":   {scope: "Namespaced", specOwner: "WorkloadOwner"},
	"WorkloadAdapter":    {scope: "Namespaced", specOwner: "Operator"},
}

var requiredConditions = []string{"Accepted", "ResolvedRefs", "Programmed", "Ready"}

var requiredCoreFeatures = []string{
	"networking.waycloak.io/CoreFailClosedEgress",
	"networking.waycloak.io/TCP",
	"networking.waycloak.io/UDP",
	"networking.waycloak.io/DNSContainment",
	"networking.waycloak.io/GatewayReplacementRecovery",
	"networking.waycloak.io/NodeRestartRecovery",
}

type kindExpectation struct {
	scope     string
	specOwner string
}

type contract struct {
	SchemaVersion               int                 `json:"schemaVersion"`
	APIGroup                    string              `json:"apiGroup"`
	APIVersion                  string              `json:"apiVersion"`
	MinimumKubernetesVersion    string              `json:"minimumKubernetesVersion"`
	UnknownFields               string              `json:"unknownFields"`
	ClaimsGatewayAPIConformance bool                `json:"claimsGatewayAPIConformance"`
	ReferenceGrantCore          bool                `json:"referenceGrantCore"`
	CommonConditions            []string            `json:"commonConditions"`
	ConditionReasons            map[string][]string `json:"conditionReasons"`
	FeatureChannels             map[string][]string `json:"featureChannels"`
	FieldManagers               []fieldManager      `json:"fieldManagers"`
	Kinds                       []kindContract      `json:"kinds"`
}

type fieldManager struct {
	Name string `json:"name"`
	Owns string `json:"owns"`
}

type kindContract struct {
	Kind            string           `json:"kind"`
	Scope           string           `json:"scope"`
	SpecOwner       string           `json:"specOwner"`
	StatusOwner     string           `json:"statusOwner"`
	Fields          []string         `json:"fields"`
	Lists           []listContract   `json:"lists"`
	References      []reference      `json:"references"`
	ImmutableFields []string         `json:"immutableFields"`
	OwnerReferences []ownerReference `json:"ownerReferences"`
	Finalizers      []finalizer      `json:"finalizers"`
	Conditions      []string         `json:"conditions"`
}

type listContract struct {
	Path     string   `json:"path"`
	Type     string   `json:"type"`
	Keys     []string `json:"keys"`
	MinItems int      `json:"minItems"`
	MaxItems int      `json:"maxItems"`
}

type reference struct {
	Path          string `json:"path"`
	Target        string `json:"target"`
	NamespaceRule string `json:"namespaceRule"`
	Existence     string `json:"existence"`
	Compatibility string `json:"compatibility"`
	Consent       string `json:"consent"`
	Revocation    string `json:"revocation"`
	Privacy       string `json:"privacy"`
}

type ownerReference struct {
	Target                    string `json:"target"`
	CrossNamespace            bool   `json:"crossNamespace"`
	ControllerOwnedUserIntent bool   `json:"controllerOwnedUserIntent"`
}

type finalizer struct {
	Name            string `json:"name"`
	ExternalCleanup bool   `json:"externalCleanup"`
	Bounded         bool   `json:"bounded"`
	MaximumDuration string `json:"maximumDuration"`
	TimeoutOutcome  string `json:"timeoutOutcome"`
}

func main() {
	path := flag.String("contract", defaultContract, "path to the replacement API freeze contract")
	flag.Parse()
	data, err := os.ReadFile(*path)
	if err != nil {
		fail(fmt.Errorf("read API freeze contract: %w", err))
	}
	if err := audit(data); err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

func audit(data []byte) error {
	for _, forbidden := range []string{"v1alpha", "networking.waycloak.io/gateway", "allocation-configmap", "waycloak-prepare", "waycloak-verify"} {
		if strings.Contains(string(data), forbidden) {
			return fmt.Errorf("freeze contract contains forbidden alpha surface %q", forbidden)
		}
	}
	var value contract
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return fmt.Errorf("decode API freeze contract: %w", err)
	}
	if value.SchemaVersion != 1 || value.APIGroup != "networking.waycloak.io" || value.APIVersion != "v1beta1" || value.MinimumKubernetesVersion != "1.36.0" {
		return errors.New("group, version, Kubernetes minimum, or schema version is not frozen")
	}
	if value.UnknownFields != "Reject" || value.ClaimsGatewayAPIConformance || value.ReferenceGrantCore {
		return errors.New("unknown-field, Gateway API, or ReferenceGrant Core boundary is unsafe")
	}
	if !slices.Equal(value.CommonConditions, requiredConditions) {
		return fmt.Errorf("common conditions = %v, want %v", value.CommonConditions, requiredConditions)
	}
	for _, condition := range requiredConditions {
		if len(value.ConditionReasons[condition]) == 0 {
			return fmt.Errorf("condition %s has no stable reasons", condition)
		}
	}
	for _, channel := range []string{"Core", "Extended", "Experimental"} {
		if _, ok := value.FeatureChannels[channel]; !ok {
			return fmt.Errorf("feature channel %s is not declared", channel)
		}
	}
	if !slices.Equal(value.FeatureChannels["Core"], requiredCoreFeatures) || len(value.FieldManagers) != len(requiredKinds) {
		return errors.New("core features or field managers are incomplete")
	}
	managerNames := map[string]bool{}
	for _, manager := range value.FieldManagers {
		if manager.Name == "" || manager.Owns == "" || managerNames[manager.Name] {
			return fmt.Errorf("field manager %q is empty or duplicated", manager.Name)
		}
		managerNames[manager.Name] = true
	}
	if len(value.Kinds) != len(requiredKinds) {
		return fmt.Errorf("kind count = %d, want %d", len(value.Kinds), len(requiredKinds))
	}
	seenKinds := map[string]bool{}
	for _, kind := range value.Kinds {
		expectation, ok := requiredKinds[kind.Kind]
		if !ok || seenKinds[kind.Kind] {
			return fmt.Errorf("kind %q is unexpected or duplicated", kind.Kind)
		}
		seenKinds[kind.Kind] = true
		if kind.Scope != expectation.scope || kind.SpecOwner != expectation.specOwner || kind.StatusOwner == "" {
			return fmt.Errorf("kind %s ownership or scope is not frozen", kind.Kind)
		}
		if !slices.Equal(kind.Conditions, requiredConditions) {
			return fmt.Errorf("kind %s does not implement common conditions", kind.Kind)
		}
		fields := map[string]bool{}
		for _, field := range kind.Fields {
			if field == "" || fields[field] || !(strings.HasPrefix(field, "spec.") || strings.HasPrefix(field, "status.")) {
				return fmt.Errorf("kind %s field %q is invalid or duplicated", kind.Kind, field)
			}
			fields[field] = true
		}
		if !fields["status.conditions"] {
			return fmt.Errorf("kind %s omits status.conditions", kind.Kind)
		}
		for _, list := range kind.Lists {
			if !fields[list.Path] || !slices.Contains([]string{"atomic", "set", "map"}, list.Type) || list.MaxItems < 1 || list.MinItems < 0 || list.MinItems > list.MaxItems {
				return fmt.Errorf("kind %s list %s is not structurally bounded", kind.Kind, list.Path)
			}
			if list.Type == "map" && len(list.Keys) == 0 || list.Type != "map" && len(list.Keys) != 0 {
				return fmt.Errorf("kind %s list %s has invalid map keys", kind.Kind, list.Path)
			}
		}
		for _, ref := range kind.References {
			if !fields[ref.Path] || ref.Target == "" || ref.NamespaceRule == "" || ref.Existence == "" || ref.Compatibility == "" || ref.Consent == "" || ref.Revocation == "" || ref.Privacy == "" {
				return fmt.Errorf("kind %s reference %s lacks complete semantics", kind.Kind, ref.Path)
			}
		}
		for _, immutable := range kind.ImmutableFields {
			if !fields[immutable] {
				return fmt.Errorf("kind %s immutable field %s is not declared", kind.Kind, immutable)
			}
		}
		for _, owner := range kind.OwnerReferences {
			if owner.Target == "" || owner.CrossNamespace || owner.ControllerOwnedUserIntent {
				return fmt.Errorf("kind %s has unsafe owner reference", kind.Kind)
			}
		}
		for _, finalizer := range kind.Finalizers {
			if finalizer.Name == "" || !finalizer.ExternalCleanup || !finalizer.Bounded || finalizer.MaximumDuration == "" || finalizer.TimeoutOutcome == "" {
				return fmt.Errorf("kind %s has unbounded or non-external finalizer", kind.Kind)
			}
		}
	}
	route := findKind(value.Kinds, "VPNEgressRoute")
	if route == nil || !hasList(route.Lists, "spec.parentRefs", "map", 1) {
		return errors.New("VPNEgressRoute must have exactly one map-keyed Core parent")
	}
	if !hasList(route.Lists, "status.parents", "atomic", 1) {
		return errors.New("VPNEgressRoute status.parents must be an at-most-one atomic list")
	}
	binding := findKind(value.Kinds, "VPNWorkloadBinding")
	if binding == nil || binding.SpecOwner != "ControllerOnly" || len(binding.OwnerReferences) != 1 || len(binding.Finalizers) != 1 {
		return errors.New("VPNWorkloadBinding ownership and cleanup are incomplete")
	}
	return nil
}

func findKind(kinds []kindContract, name string) *kindContract {
	for index := range kinds {
		if kinds[index].Kind == name {
			return &kinds[index]
		}
	}
	return nil
}

func hasList(lists []listContract, path, listType string, maxItems int) bool {
	for _, list := range lists {
		if list.Path == path && list.Type == listType && list.MaxItems == maxItems {
			return true
		}
	}
	return false
}
