// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package delivery

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPublishedAdapterConformanceFixtures(t *testing.T) {
	root := filepath.Join("..", "..", "protocol", "adapter", "v1alpha1", "fixtures")
	loadDocument := func(name string) Document {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		var document Document
		if err := json.Unmarshal(data, &document); err != nil {
			t.Fatal(err)
		}
		return document
	}
	loadAcknowledgement := func(name string) ApplicationAcknowledgement {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		var acknowledgement ApplicationAcknowledgement
		if err := json.Unmarshal(data, &acknowledgement); err != nil {
			t.Fatal(err)
		}
		return acknowledgement
	}

	current := loadDocument("current.json")
	if err := current.Validate(time.Date(2030, 1, 1, 0, 0, 30, 0, time.UTC)); err != nil {
		t.Fatalf("current fixture: %v", err)
	}
	rotated := loadDocument("rotated.json")
	if err := rotated.Validate(time.Date(2030, 1, 1, 0, 1, 30, 0, time.UTC)); err != nil {
		t.Fatalf("rotated fixture: %v", err)
	}
	if err := loadDocument("expired.json").Validate(time.Date(2030, 1, 1, 0, 0, 30, 0, time.UTC)); err == nil {
		t.Fatal("expired fixture was current")
	}
	if err := loadDocument("duplicate.json").Validate(time.Date(2030, 1, 1, 0, 0, 30, 0, time.UTC)); err == nil {
		t.Fatal("duplicate fixture was valid")
	}
	missing := loadDocument("missing.json")
	if err := missing.Validate(time.Date(2030, 1, 1, 0, 0, 30, 0, time.UTC)); err != nil || len(missing.Leases) != 0 {
		t.Fatalf("missing fixture = %#v error=%v", missing, err)
	}

	store := &Store{Now: func() time.Time { return time.Date(2030, 1, 1, 0, 0, 30, 0, time.UTC) }, document: current}
	if err := store.Acknowledge("lease-uid-1", loadAcknowledgement("current-ack.json")); err != nil {
		t.Fatalf("current acknowledgement: %v", err)
	}
	if err := store.Acknowledge("lease-uid-1", loadAcknowledgement("wrong-pod-uid-ack.json")); !errors.Is(err, ErrAcknowledgementMismatch) {
		t.Fatalf("wrong-Pod acknowledgement error = %v", err)
	}
	store.document = rotated
	store.Now = func() time.Time { return time.Date(2030, 1, 1, 0, 1, 30, 0, time.UTC) }
	if err := store.Acknowledge("lease-uid-1", loadAcknowledgement("current-ack.json")); !errors.Is(err, ErrAcknowledgementMismatch) {
		t.Fatalf("stale-generation acknowledgement error = %v", err)
	}
	if err := store.Acknowledge("lease-uid-1", loadAcknowledgement("rotated-ack.json")); err != nil {
		t.Fatalf("rotated acknowledgement: %v", err)
	}
}
