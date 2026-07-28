// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package qbittorrentadapter

import (
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	wayportforward "github.com/Amoenus/waycloak/internal/portforward"
)

func TestPublishedAdapterSchemaTracksProtocolMessages(t *testing.T) {
	contents, err := os.ReadFile("../../docs/api/schemas/workload-adapter-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Definitions map[string]struct {
			Required []string `json:"required"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(contents, &schema); err != nil {
		t.Fatal(err)
	}
	for name, message := range map[string]any{
		"leaseRecord":               wayportforward.AdapterLeaseRecord{},
		"acknowledgement":           wayportforward.AdapterAcknowledgement{},
		"withdrawalAcknowledgement": wayportforward.AdapterWithdrawalAcknowledgement{},
		"health":                    wayportforward.AdapterHealthObservation{},
	} {
		definition, ok := schema.Definitions[name]
		if !ok {
			t.Fatalf("published adapter schema has no %s definition", name)
		}
		want := jsonFieldNames(reflect.TypeOf(message))
		got := append([]string(nil), definition.Required...)
		sort.Strings(got)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s required fields = %v, want protocol fields %v", name, got, want)
		}
	}
}

func jsonFieldNames(value reflect.Type) []string {
	fields := make([]string, 0, value.NumField())
	for index := range value.NumField() {
		name := strings.Split(value.Field(index).Tag.Get("json"), ",")[0]
		if name != "" && name != "-" {
			fields = append(fields, name)
		}
	}
	sort.Strings(fields)
	return fields
}
