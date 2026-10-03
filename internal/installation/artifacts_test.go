// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package installation

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sigstore/sigstore-go/pkg/root"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestVerifyPublishedRelease(t *testing.T) {
	trusted, err := root.NewTrustedRootFromJSON(fixture(t, "trusted-root.json"))
	if err != nil {
		t.Fatal(err)
	}
	resolver := ReleaseResolver{TrustedMaterial: trusted}
	artifact, signature := fixture(t, "release-manifest.json"), fixture(t, "release-manifest.sigstore.json")
	manifest, err := resolver.Verify("v1.0.2-rc.3", artifact, signature)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Version != "v1.0.2-rc.3" {
		t.Fatal("unexpected release")
	}
	for _, test := range []struct {
		name, version       string
		artifact, signature []byte
	}{
		{"different tag", "v1.0.2-rc.2", artifact, signature},
		{"altered artifact", "v1.0.2-rc.3", append(append([]byte{}, artifact...), '\n'), signature},
		{"missing signature", "v1.0.2-rc.3", artifact, nil},
		{"unversioned tag", "latest", artifact, signature},
		{"path traversal", "../v1.0.2-rc.3", artifact, signature},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := resolver.Verify(test.version, test.artifact, test.signature); err == nil {
				t.Fatal("untrusted release accepted")
			}
		})
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestResolveDownloadsOnlyExactPublishedAssets(t *testing.T) {
	trusted, err := root.NewTrustedRootFromJSON(fixture(t, "trusted-root.json"))
	if err != nil {
		t.Fatal(err)
	}
	var requested []string
	r := ReleaseResolver{TrustedMaterial: trusted, Client: &http.Client{Transport: roundTrip(func(req *http.Request) (*http.Response, error) {
		requested = append(requested, req.URL.String())
		name := "release-manifest.json"
		if strings.HasSuffix(req.URL.Path, "release-manifest.sigstore.json") {
			name = "release-manifest.sigstore.json"
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(fixture(t, name)))}, nil
	})}}
	if _, err := r.Resolve(context.Background(), "v1.0.2-rc.3"); err != nil {
		t.Fatal(err)
	}
	want := releaseRepository + "/releases/download/v1.0.2-rc.3/"
	if len(requested) != 2 || requested[0] != want+"release-manifest.json" || requested[1] != want+"release-manifest.sigstore.json" {
		t.Fatalf("unexpected assets: %v", requested)
	}
	if _, err := r.Resolve(context.Background(), "../../other"); err == nil || len(requested) != 2 {
		t.Fatal("invalid version reached network")
	}
}

func TestDownloadRejectsFailureAndOversize(t *testing.T) {
	for _, code := range []int{200, 404, 500} {
		r := ReleaseResolver{Client: &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader("oversized"))}, nil
		})}}
		if _, err := r.fetch(context.Background(), releaseRepository, 3); err == nil {
			t.Fatalf("accepted response %d", code)
		}
	}
}

// Opt-in network integration against immutable public artifacts. It never
// creates Kubernetes clients or changes a cluster.
func TestPublishedArtifactsWithoutExecutables(t *testing.T) {
	if os.Getenv("WAYCLOAK_VERIFY_PUBLIC_ARTIFACTS") != "1" {
		t.Skip("set WAYCLOAK_VERIFY_PUBLIC_ARTIFACTS=1 for the public registry integration check")
	}
	t.Setenv("PATH", "")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	resolver := ReleaseResolver{}
	manifest, err := resolver.Resolve(ctx, "v1.0.2-rc.3")
	if err != nil {
		t.Fatal(err)
	}
	runtime := HelmRuntime{}
	identities, err := runtime.CRDIdentities(ctx, manifest.Chart)
	if err != nil {
		t.Fatal(err)
	}
	if len(identities) != 6 {
		t.Fatalf("unexpected networking API inventory: %d", len(identities))
	}
}
