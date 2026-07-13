//go:build envtest

package integration

import (
	"path/filepath"
	"testing"

	wayv1 "github.com/Amoenus/waycloak/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

func TestCRDsInstall(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := wayv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	e := &envtest.Environment{CRDDirectoryPaths: []string{filepath.Join("..", "..", "config", "crd", "bases")}}
	cfg, err := e.Start()
	if err != nil {
		t.Fatal(err)
	}
	if cfg == nil {
		t.Fatal("nil config")
	}
	if err := e.Stop(); err != nil {
		t.Fatal(err)
	}
}
