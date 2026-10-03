// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package installation

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"time"

	"github.com/Amoenus/waycloak/internal/waycloakctl"
	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/tuf"
	"github.com/sigstore/sigstore-go/pkg/verify"
)

var releaseVersion = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?$`)

const releaseRepository = "https://github.com/Amoenus/waycloak"

// ReleaseResolver accepts only releases signed by the project's publication
// workflow at the exact requested tag. Neither URLs nor signer policies come
// from the installation resource.
type ReleaseResolver struct {
	Client          *http.Client
	TrustedMaterial root.TrustedMaterial
}

func (r *ReleaseResolver) Resolve(ctx context.Context, version string) (waycloakctl.ReleaseManifest, error) {
	if !releaseVersion.MatchString(version) {
		return waycloakctl.ReleaseManifest{}, errors.New("release version must be an explicit semantic version tag")
	}
	base := releaseRepository + "/releases/download/" + version + "/"
	artifact, err := r.fetch(ctx, base+"release-manifest.json", 1<<20)
	if err != nil {
		return waycloakctl.ReleaseManifest{}, err
	}
	signature, err := r.fetch(ctx, base+"release-manifest.sigstore.json", 4<<20)
	if err != nil {
		return waycloakctl.ReleaseManifest{}, err
	}
	return r.Verify(version, artifact, signature)
}

// Verify is also used for journal snapshots after controller restart. A stored
// artifact is never trusted merely because it was placed in a ConfigMap.
func (r *ReleaseResolver) Verify(version string, artifact, signature []byte) (waycloakctl.ReleaseManifest, error) {
	if !releaseVersion.MatchString(version) || len(artifact) > 1<<20 || len(signature) > 4<<20 {
		return waycloakctl.ReleaseManifest{}, errors.New("invalid or oversized release artifact")
	}
	trusted := r.TrustedMaterial
	if trusted == nil {
		client, err := tuf.New(tuf.DefaultOptions())
		if err != nil {
			return waycloakctl.ReleaseManifest{}, fmt.Errorf("initialize release trust: %w", err)
		}
		trusted, err = root.GetTrustedRoot(client)
		if err != nil {
			return waycloakctl.ReleaseManifest{}, fmt.Errorf("refresh release trust: %w", err)
		}
	}
	verifier, err := verify.NewVerifier(trusted, verify.WithSignedCertificateTimestamps(1), verify.WithTransparencyLog(1), verify.WithObserverTimestamps(1))
	if err != nil {
		return waycloakctl.ReleaseManifest{}, err
	}
	identity, err := verify.NewShortCertificateIdentity("https://token.actions.githubusercontent.com", "", releaseRepository+"/.github/workflows/waycloak-release.yaml@refs/tags/"+version, "")
	if err != nil {
		return waycloakctl.ReleaseManifest{}, err
	}
	var signed bundle.Bundle
	if err := signed.UnmarshalJSON(signature); err != nil {
		return waycloakctl.ReleaseManifest{}, errors.New("invalid release signature bundle")
	}
	if _, err := verifier.Verify(&signed, verify.NewPolicy(verify.WithArtifact(bytes.NewReader(artifact)), verify.WithCertificateIdentity(identity))); err != nil {
		return waycloakctl.ReleaseManifest{}, fmt.Errorf("release signature verification failed: %w", err)
	}
	manifest, _, err := waycloakctl.DecodeReleaseManifest(artifact)
	if err != nil {
		return waycloakctl.ReleaseManifest{}, err
	}
	if manifest.Version != version {
		return waycloakctl.ReleaseManifest{}, errors.New("signed manifest version differs from requested release")
	}
	return manifest, nil
}

func (r *ReleaseResolver) fetch(ctx context.Context, url string, limit int64) ([]byte, error) {
	client := r.Client
	if client == nil {
		client = &http.Client{Timeout: 45 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if req.URL.Scheme != "https" || len(via) >= 5 {
				return errors.New("release download refused an insecure or excessive redirect")
			}
			return nil
		}}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(req)
	if err != nil {
		return nil, errors.New("release artifact download failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("release artifact download returned HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, errors.New("release artifact unreadable or exceeds size limit")
	}
	return data, nil
}
