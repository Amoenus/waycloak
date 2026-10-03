// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package waycloakctl

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestCommandRuntimePreservesInterruptionCheckpointFiles(t *testing.T) {
	for _, test := range []struct{ name, overrides, filename string }{
		{"bootstrap", controllerFirstBootstrapValues, "controller-first-bootstrap.yaml"},
		{"held staging", "nodeAgent:\n  observationCapabilityHold: true\n", "node-agent-transition-hold.yaml"},
		{"activation", "", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			var files []string
			called := false
			runtime := commandInstallRuntime{run: func(_ context.Context, name string, args ...string) ([]byte, error) {
				called = true
				if name != "helm" {
					t.Fatalf("unexpected executable %s", name)
				}
				for index, arg := range args {
					if arg == "--values" {
						files = append(files, args[index+1])
					}
				}
				want := 1
				if test.filename != "" {
					want = 2
				}
				if len(files) != want {
					t.Fatalf("values files: %v", files)
				}
				if want == 2 {
					if filepath.Base(files[1]) != test.filename {
						t.Fatalf("interruption checkpoint changed: %s", files[1])
					}
					data, err := os.ReadFile(files[1])
					if err != nil || string(data) != test.overrides {
						t.Fatalf("phase override changed: %v", err)
					}
				}
				return nil, nil
			}}
			if err := runtime.Apply(context.Background(), InstallPlan{Values: "controller: {enabled: true}\n"}, test.overrides); err != nil {
				t.Fatal(err)
			}
			if !called {
				t.Fatal("Helm was not invoked")
			}
			for _, file := range files {
				if _, err := os.Stat(file); !os.IsNotExist(err) {
					t.Fatalf("temporary values were not removed: %v", err)
				}
			}
		})
	}
}
