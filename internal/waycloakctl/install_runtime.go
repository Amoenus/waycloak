// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package waycloakctl

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// InstallRuntime is the artifact and resource-application boundary of the
// fail-closed lifecycle. Implementations must apply exactly the supplied chart
// digest and values, without automatically rolling back or removing the hold.
// The in-cluster reconciler uses a library implementation; the optional CLI
// retains its existing Helm executable implementation.
type InstallRuntime interface {
	CRDIdentities(context.Context, Artifact) (map[string]string, error)
	Apply(context.Context, InstallPlan, string) error
}

type commandInstallRuntime struct {
	run func(context.Context, string, ...string) ([]byte, error)
}

func (r commandInstallRuntime) CRDIdentities(ctx context.Context, chart Artifact) (map[string]string, error) {
	return ChartCRDIdentities(ctx, r.run, chart)
}

func (r commandInstallRuntime) Apply(ctx context.Context, plan InstallPlan, overrides string) error {
	directory, err := os.MkdirTemp("", "waycloak-install-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(directory)
	paths := []string{filepath.Join(directory, "values.yaml")}
	if err := os.WriteFile(paths[0], []byte(plan.Values), 0o600); err != nil {
		return err
	}
	if overrides != "" {
		path := filepath.Join(directory, "lifecycle-overrides.yaml")
		if err := os.WriteFile(path, []byte(overrides), 0o600); err != nil {
			return err
		}
		paths = append(paths, path)
	}
	run := r.run
	if run == nil {
		run = defaultRunner
	}
	output, err := run(ctx, "helm", helmUpgradeArguments(plan, plan.Chart.Repository+"@"+plan.Chart.Digest, paths...)...)
	if err != nil {
		return fmt.Errorf("apply exact chart: %w: %s", err, bounded(output, 4096))
	}
	return nil
}
