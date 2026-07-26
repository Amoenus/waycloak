// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package main

import (
	"strings"
	"testing"
)

func TestRenderReplacementReference(t *testing.T) {
	data, err := render("../../config/crd/bases")
	if err != nil {
		t.Fatal(err)
	}
	reference := string(data)
	for _, value := range append([]string{"networking.waycloak.io/v1beta1", "Unknown fields are rejected"}, kindOrder...) {
		if !strings.Contains(reference, value) {
			t.Errorf("reference does not contain %q", value)
		}
	}
	if strings.Contains(strings.ToLower(reference), "v1alpha") {
		t.Fatal("reference contains an alpha API marker")
	}
}

func TestSchemaType(t *testing.T) {
	if got := schemaType(jsonSchema{Type: "array", Items: &jsonSchema{Type: "string"}}); got != "array<string>" {
		t.Fatalf("schemaType() = %q", got)
	}
	if got := schemaType(jsonSchema{IntOrString: true}); got != "integer|string" {
		t.Fatalf("schemaType() = %q", got)
	}
}
