// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package installation

import (
	"context"
	"errors"
	"reflect"
	"strings"

	installv1 "github.com/Amoenus/waycloak/api/installation/v1alpha1"
	"github.com/Amoenus/waycloak/internal/waycloakctl"
	"github.com/Masterminds/semver/v3"
	"k8s.io/apimachinery/pkg/types"
)

type Backend struct {
	Clients  *waycloakctl.Clients
	Resolver *ReleaseResolver
	Runtime  *HelmRuntime
	verified map[string]waycloakctl.ReleaseManifest
}

func (b *Backend) resolve(ctx context.Context, version string) (waycloakctl.ReleaseManifest, error) {
	if manifest, ok := b.verified[version]; ok {
		return manifest, nil
	}
	manifest, err := b.Resolver.Resolve(ctx, version)
	if err != nil {
		return manifest, err
	}
	// Old builds remove namespace denial before terminating applications stop.
	// They are valid adoption sources, never safe rollback targets.
	parsed, err := semver.StrictNewVersion(strings.TrimPrefix(version, "v"))
	if err != nil || parsed.LessThan(semver.MustParse("1.0.2-rc.3")) {
		return manifest, errors.New("release predates the terminating-namespace fail-closed fix; select v1.0.2-rc.3 or a successor")
	}
	if b.verified == nil {
		b.verified = map[string]waycloakctl.ReleaseManifest{}
	}
	if len(b.verified) >= 2 {
		b.verified = map[string]waycloakctl.ReleaseManifest{}
	}
	b.verified[version] = manifest
	return manifest, nil
}

func (b *Backend) Prepare(ctx context.Context, spec installv1.WaycloakInstallationSpec) (waycloakctl.NativeInstallPlan, error) {
	manifest, err := b.resolve(ctx, spec.Version)
	if err != nil {
		return waycloakctl.NativeInstallPlan{}, err
	}
	if spec.Namespace != b.Runtime.Namespace {
		return waycloakctl.NativeInstallPlan{}, errors.New("installation namespace differs from the controller's configured runtime namespace")
	}
	plan, err := waycloakctl.PrepareNativeInstall(ctx, b.Clients, b.Runtime, spec, manifest)
	if err != nil {
		return plan, err
	}
	if spec.Version == "v1.0.2-rc.3" && plan.Plan.Source.ManifestDigest == manifest.ManifestDigest {
		return plan, errors.New("configuration-only transitions require the declarative lifecycle release or a successor")
	}
	if err := b.Runtime.Validate(ctx, plan.Plan); err != nil {
		return plan, err
	}
	return plan, nil
}

func (b *Backend) Advance(ctx context.Context, uid types.UID, snapshot waycloakctl.NativeInstallPlan, phase string) (string, error) {
	manifest, err := b.resolve(ctx, snapshot.Plan.Target.Version)
	if err != nil {
		return phase, err
	}
	if !reflect.DeepEqual(manifest, snapshot.Plan.Target) {
		return phase, errors.New("journal target differs from the signed publication")
	}
	b.Runtime.Operation = &Operation{InstallationUID: uid, PlanID: snapshot.Plan.PlanID, Phase: phase}
	return waycloakctl.AdvanceNativeInstall(ctx, b.Clients, b.Runtime, snapshot, phase)
}

func (b *Backend) Verify(ctx context.Context, snapshot waycloakctl.NativeInstallPlan) error {
	manifest, err := b.resolve(ctx, snapshot.Plan.Target.Version)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(manifest, snapshot.Plan.Target) {
		return errors.New("completed target differs from signed publication")
	}
	return waycloakctl.VerifyNativeInstall(ctx, b.Clients, snapshot)
}
