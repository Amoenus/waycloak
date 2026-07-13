//go:build e2e

package e2e

import (
	"os/exec"
	"testing"
)

// This suite targets a disposable Kind cluster prepared by the project e2e
// harness. It deliberately verifies API-level startup blocking without claiming
// that the Phase 1 slice implements packet protection.
func TestClusterIsKind(t *testing.T) {
	out, err := exec.Command("kubectl", "config", "current-context").CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	if len(out) < 5 || string(out[:5]) != "kind-" {
		t.Fatalf("e2e tests require a disposable Kind context, got %q", out)
	}
}
