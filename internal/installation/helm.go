// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package installation

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/Amoenus/waycloak/internal/waycloakctl"
	"helm.sh/helm/v4/pkg/action"
	"helm.sh/helm/v4/pkg/chart/loader"
	chartv2 "helm.sh/helm/v4/pkg/chart/v2"
	"helm.sh/helm/v4/pkg/kube"
	"helm.sh/helm/v4/pkg/registry"
	"helm.sh/helm/v4/pkg/storage/driver"
	"oras.land/oras-go/v2/registry/remote/auth"
	"sigs.k8s.io/yaml"
)

var regexpDigest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// HelmRuntime applies the existing runtime chart without executing a shell,
// helm, or waycloakctl. Its caller serializes actions with a Kubernetes Lease.
type HelmRuntime struct {
	Configuration *action.Configuration
	Namespace     string
	charts        map[waycloakctl.Artifact]*chartv2.Chart
}

func (r *HelmRuntime) chart(ctx context.Context, artifact waycloakctl.Artifact) (*chartv2.Chart, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !strings.HasPrefix(artifact.Repository, "oci://ghcr.io/amoenus/") || !regexpDigest.MatchString(artifact.Digest) {
		return nil, errors.New("runtime chart must be an exact project OCI artifact")
	}
	if chart := r.charts[artifact]; chart != nil {
		return chart, nil
	}
	httpClient := &http.Client{Timeout: 45 * time.Second}
	// Published artifacts are public. Do not inherit an operator's Docker
	// credentials or invoke an external credential-helper executable.
	authorizer := auth.Client{Client: httpClient, Cache: auth.NewCache(), Credential: func(context.Context, string) (auth.Credential, error) {
		return auth.EmptyCredential, nil
	}}
	client, err := registry.NewClient(registry.ClientOptHTTPClient(httpClient), registry.ClientOptAuthorizer(authorizer), registry.ClientOptRegistryAuthorizer(&authorizer))
	if err != nil {
		return nil, err
	}
	pulled, err := client.Pull(strings.TrimPrefix(artifact.Repository, "oci://") + "@" + artifact.Digest)
	if err != nil {
		return nil, fmt.Errorf("pull exact runtime chart: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if pulled.Manifest == nil || pulled.Manifest.Digest != artifact.Digest || pulled.Chart == nil {
		return nil, errors.New("runtime chart registry digest does not match verified release")
	}
	loaded, err := loader.LoadArchive(bytes.NewReader(pulled.Chart.Data))
	if err != nil {
		return nil, err
	}
	chart, ok := loaded.(*chartv2.Chart)
	if !ok {
		return nil, errors.New("unsupported runtime chart format")
	}
	// Keep only the current exact artifact: an unbounded release history must
	// not turn into an unbounded resident archive cache.
	r.charts = map[waycloakctl.Artifact]*chartv2.Chart{artifact: chart}
	return chart, nil
}

func (r *HelmRuntime) CRDIdentities(ctx context.Context, artifact waycloakctl.Artifact) (map[string]string, error) {
	chart, err := r.chart(ctx, artifact)
	if err != nil {
		return nil, err
	}
	var documents bytes.Buffer
	for _, crd := range chart.CRDObjects() {
		documents.WriteString("---\n")
		documents.Write(crd.File.Data)
		documents.WriteByte('\n')
	}
	return waycloakctl.DecodeChartCRDIdentities(documents.Bytes())
}

func (r *HelmRuntime) Apply(ctx context.Context, plan waycloakctl.InstallPlan, overrides string) error {
	if r.Configuration == nil || r.Namespace == "" || plan.Namespace != r.Namespace {
		return errors.New("runtime namespace does not match installation")
	}
	chart, err := r.chart(ctx, plan.Chart)
	if err != nil {
		return err
	}
	values, err := mergeValues(plan.Values, overrides)
	if err != nil {
		return err
	}
	_, err = r.Configuration.Releases.Last(plan.Release)
	if errors.Is(err, driver.ErrReleaseNotFound) {
		install := action.NewInstall(r.Configuration)
		install.ReleaseName, install.Namespace = plan.Release, plan.Namespace
		install.ServerSideApply, install.ForceConflicts = true, true
		install.WaitStrategy, install.Timeout = kube.StatusWatcherStrategy, 10*time.Minute
		_, err = install.RunWithContext(ctx, chart, values)
		return err
	}
	if err != nil {
		return err
	}
	upgrade := action.NewUpgrade(r.Configuration)
	upgrade.Namespace = plan.Namespace
	upgrade.ServerSideApply, upgrade.ForceConflicts = "true", true
	upgrade.WaitStrategy, upgrade.Timeout = kube.StatusWatcherStrategy, 10*time.Minute
	_, err = upgrade.RunWithContext(ctx, plan.Release, chart, values)
	return err
}

func mergeValues(base, overrides string) (map[string]any, error) {
	var values, patch map[string]any
	if err := yaml.UnmarshalStrict([]byte(base), &values); err != nil {
		return nil, errors.New("invalid runtime values")
	}
	if err := yaml.UnmarshalStrict([]byte(overrides), &patch); err != nil {
		return nil, errors.New("invalid lifecycle override values")
	}
	if values == nil {
		values = map[string]any{}
	}
	mergeMaps(values, patch)
	return values, nil
}

func mergeMaps(destination, source map[string]any) {
	for key, value := range source {
		if nested, ok := value.(map[string]any); ok {
			if existing, ok := destination[key].(map[string]any); ok {
				mergeMaps(existing, nested)
				continue
			}
		}
		destination[key] = value
	}
}
