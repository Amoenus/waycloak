// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package installation

import (
	"bytes"
	"context"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/Amoenus/waycloak/internal/waycloakctl"
	"helm.sh/helm/v4/pkg/action"
	chartutil "helm.sh/helm/v4/pkg/chart/common/util"
	"helm.sh/helm/v4/pkg/chart/loader"
	chartv2 "helm.sh/helm/v4/pkg/chart/v2"
	"helm.sh/helm/v4/pkg/kube"
	"helm.sh/helm/v4/pkg/registry"
	releasecommon "helm.sh/helm/v4/pkg/release/common"
	releasev1 "helm.sh/helm/v4/pkg/release/v1"
	"helm.sh/helm/v4/pkg/storage/driver"
	"k8s.io/apimachinery/pkg/types"
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
	// Set only by the elected installation reconciler for an immutable journal.
	Operation *Operation
}

type Operation struct {
	InstallationUID types.UID
	PlanID          string
	Phase           string
}

func (o *Operation) labels() (map[string]string, error) {
	if o == nil {
		return nil, nil
	}
	if o.InstallationUID == "" || !regexpDigest.MatchString(o.PlanID) {
		return nil, errors.New("installation operation identity is incomplete")
	}
	if o.Phase != waycloakctl.NativePhaseStage && o.Phase != waycloakctl.NativePhaseActivate {
		return nil, errors.New("installation phase does not permit a Helm operation")
	}
	data, _ := hex.DecodeString(strings.TrimPrefix(o.PlanID, "sha256:"))
	return map[string]string{"installation.waycloak.io/uid": string(o.InstallationUID), "installation.waycloak.io/plan": base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(data), "installation.waycloak.io/phase": o.Phase}, nil
}

func (r *HelmRuntime) Validate(ctx context.Context, plan waycloakctl.InstallPlan) error {
	chart, err := r.chart(ctx, plan.Chart)
	if err != nil {
		return err
	}
	values, err := mergeValues(plan.Values, "")
	if err != nil {
		return err
	}
	coalesced, err := chartutil.CoalesceValues(chart, values)
	if err != nil {
		return err
	}
	return chartutil.ValidateAgainstSchema(chart, coalesced)
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
	labels, err := r.Operation.labels()
	if err != nil {
		return err
	}
	last, err := r.Configuration.Releases.Last(plan.Release)
	if errors.Is(err, driver.ErrReleaseNotFound) {
		install := action.NewInstall(r.Configuration)
		install.ReleaseName, install.Namespace = plan.Release, plan.Namespace
		install.Labels = labels
		install.ServerSideApply, install.ForceConflicts = true, true
		install.WaitStrategy, install.Timeout = kube.StatusWatcherStrategy, 10*time.Minute
		_, err = install.RunWithContext(ctx, chart, values)
		return err
	}
	if err != nil {
		return err
	}
	previous, ok := last.(*releasev1.Release)
	if !ok {
		return errors.New("unsupported Helm release record format")
	}
	if previous.Info.Status.IsPending() && r.Operation != nil {
		for key, value := range labels {
			if previous.Labels[key] != value {
				return errors.New("pending Helm operation is not owned by the active installation journal")
			}
		}
		// The manager's leader Lease excludes a live predecessor. Preserve the
		// record and its manifest; a normal upgrade replays the same held phase.
		previous.Info.Status = releasecommon.StatusFailed
		previous.Info.Description = "Interrupted installation phase; replaying its immutable journal under the leader Lease"
		if err := r.Configuration.Releases.Update(previous); err != nil {
			return err
		}
	}
	upgrade := action.NewUpgrade(r.Configuration)
	upgrade.Namespace = plan.Namespace
	upgrade.Labels = labels
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
