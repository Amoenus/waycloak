// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package waycloakctl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8stypes "k8s.io/apimachinery/pkg/types"
)

func TestInstallRepairPlanIsCanonicalCredentialFreeAndConfirmationBound(t *testing.T) {
	ctx := context.Background()
	clients, plan, _, _, _ := installRepairFixture(t, installCheckpointStaged)
	encoded, err := EncodeInstallRepairPlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("opaque-corrupt-helm-payload")) {
		t.Fatal("repair plan copied opaque Helm release content")
	}
	loaded, err := decodeInstallRepairPlan(encoded)
	if err != nil || !reflect.DeepEqual(loaded, plan) {
		t.Fatalf("canonical repair plan did not round-trip: %#v %v", loaded, err)
	}

	tampered := plan
	tampered.StuckRevision.Status = "failed"
	if _, err = decodeInstallRepairPlan(encodeTestJSON(t, tampered)); err == nil || !strings.Contains(err.Error(), "planID") {
		t.Fatalf("tampered repair plan was accepted: %v", err)
	}
	tampered = plan
	tampered.Security = append([]string(nil), plan.Security...)
	tampered.Security[0] = "weaken denial"
	if _, err = decodeInstallRepairPlan(encodeTestJSON(t, tampered)); err == nil || !strings.Contains(err.Error(), "instructions") {
		t.Fatalf("tampered repair instructions were accepted: %v", err)
	}
	if err = ApplyInstallRepairPlan(ctx, clients, nil, plan, "wrong"); err == nil || !strings.Contains(err.Error(), "--confirm") {
		t.Fatalf("wrong confirmation was accepted: %v", err)
	}
	if _, err = clients.Kubernetes.CoreV1().ConfigMaps(plan.Namespace).Get(ctx, installRepairJournalName(plan.Release), metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("refused repair created a journal: %v", err)
	}
	if _, err = clients.Kubernetes.CoreV1().Secrets(plan.Namespace).Get(ctx, plan.StuckRevision.Name, metav1.GetOptions{}); err != nil {
		t.Fatalf("refused repair removed the stuck revision: %v", err)
	}
}

func TestInstallRepairAppliesEveryExactCheckpoint(t *testing.T) {
	for _, checkpoint := range []string{installCheckpointClassWithdrawn, installCheckpointClassReplaced, installCheckpointStaged, installCheckpointTarget} {
		t.Run(checkpoint, func(t *testing.T) {
			ctx := context.Background()
			clients, plan, target, crds, bundle := installRepairFixture(t, checkpoint)
			calls := 0
			runner := repairRunner(t, clients, plan, target, crds, bundle, &calls, nil)
			if err := ApplyInstallRepairPlan(ctx, clients, runner, plan, plan.PlanID); err != nil {
				t.Fatal(err)
			}
			wantCalls := 1
			if checkpoint == installCheckpointClassWithdrawn || checkpoint == installCheckpointClassReplaced {
				wantCalls = 2
			}
			if calls != wantCalls {
				t.Fatalf("repair ran %d Helm upgrades, want %d", calls, wantCalls)
			}
			if _, err := clients.Kubernetes.CoreV1().Secrets(plan.Namespace).Get(ctx, plan.StuckRevision.Name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
				t.Fatalf("exact stuck revision survived repair: %v", err)
			}
			if _, _, found, err := loadInstallTransitionJournal(ctx, clients, plan.Namespace, plan.Release); err != nil || found {
				t.Fatalf("completed repair retained transition journal: found=%t err=%v", found, err)
			}
			if _, _, found, err := loadInstallRepairJournal(ctx, clients, plan.Namespace, plan.Release); err != nil || found {
				t.Fatalf("completed repair retained repair journal: found=%t err=%v", found, err)
			}
		})
	}
}

func TestInstallRepairRecoversAfterStuckRevisionDeletion(t *testing.T) {
	ctx := context.Background()
	clients, plan, target, crds, bundle := installRepairFixture(t, installCheckpointStaged)
	calls := 0
	interrupted := errors.New("simulated client interruption")
	runner := repairRunner(t, clients, plan, target, crds, bundle, &calls, interrupted)
	if err := ApplyInstallRepairPlan(ctx, clients, runner, plan, plan.PlanID); err == nil || !strings.Contains(err.Error(), interrupted.Error()) {
		t.Fatalf("interrupted repair reported success: %v", err)
	}
	if _, err := clients.Kubernetes.CoreV1().Secrets(plan.Namespace).Get(ctx, plan.StuckRevision.Name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("stuck revision deletion was not durable: %v", err)
	}
	journalPlan, journal, found, err := loadInstallRepairJournal(ctx, clients, plan.Namespace, plan.Release)
	if err != nil || !found || !reflect.DeepEqual(journalPlan, plan) || journal.Immutable == nil || !*journal.Immutable {
		t.Fatalf("interrupted repair lost immutable evidence: found=%t plan=%#v journal=%#v err=%v", found, journalPlan, journal, err)
	}
	report, err := Preflight(ctx, clients, plan.Transition.OverlayCIDR)
	if err != nil {
		t.Fatal(err)
	}
	recovered, found, err := recoverInstallRepairPlan(ctx, clients, plan.Namespace, plan.Release, report)
	if err != nil || !found || !reflect.DeepEqual(recovered, plan) {
		t.Fatalf("deleted-candidate repair was not recoverable: found=%t plan=%#v err=%v", found, recovered, err)
	}
	runner = repairRunner(t, clients, plan, target, crds, bundle, &calls, nil)
	if err = ApplyInstallRepairPlan(ctx, clients, runner, plan, plan.PlanID); err != nil {
		t.Fatal(err)
	}
}

func TestInstallRepairRecognizesHelmSuccessBeforeJournalCleanup(t *testing.T) {
	ctx := context.Background()
	clients, plan, target, crds, bundle := installRepairFixture(t, installCheckpointStaged)
	if _, err := ensureInstallRepairJournal(ctx, clients, plan); err != nil {
		t.Fatal(err)
	}
	uid := k8stypes.UID(plan.StuckRevision.UID)
	if err := clients.Kubernetes.CoreV1().Secrets(plan.Namespace).Delete(ctx, plan.StuckRevision.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}}); err != nil {
		t.Fatal(err)
	}
	seedInstalledRelease(t, clients, target, plan.Namespace, plan.Release, 3, crds)
	calls := 0
	runner := repairRunner(t, clients, plan, target, crds, bundle, &calls, errors.New("Helm must not be repeated"))
	if err := ApplyInstallRepairPlan(ctx, clients, runner, plan, plan.PlanID); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("already successful Helm activation was repeated %d times", calls)
	}
}

func TestInstallRepairRecognizesHelmSuccessReusingDeletedRevisionNumber(t *testing.T) {
	ctx := context.Background()
	clients, plan, target, crds, bundle := installRepairFixture(t, installCheckpointStaged)
	if _, err := ensureInstallRepairJournal(ctx, clients, plan); err != nil {
		t.Fatal(err)
	}
	uid := k8stypes.UID(plan.StuckRevision.UID)
	if err := clients.Kubernetes.CoreV1().Secrets(plan.Namespace).Delete(ctx, plan.StuckRevision.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}}); err != nil {
		t.Fatal(err)
	}
	seedInstalledRelease(t, clients, target, plan.Namespace, plan.Release, plan.StuckRevision.Version, crds)
	calls := 0
	runner := repairRunner(t, clients, plan, target, crds, bundle, &calls, errors.New("Helm must not be repeated"))
	if err := ApplyInstallRepairPlan(ctx, clients, runner, plan, plan.PlanID); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("already successful same-number Helm activation was repeated %d times", calls)
	}
}

func TestInstallRepairRejectsRevisionDriftAndExcludesOtherMutations(t *testing.T) {
	ctx := context.Background()
	clients, plan, _, _, bundle := installRepairFixture(t, installCheckpointStaged)
	if _, err := ensureInstallRepairJournal(ctx, clients, plan); err != nil {
		t.Fatal(err)
	}
	if err := ensureNoInstallTransition(ctx, clients, plan.Namespace, plan.Release); err == nil || !strings.Contains(err.Error(), "repair is active") {
		t.Fatalf("certificate rotation exclusion did not observe repair: %v", err)
	}
	if err := ApplyInstallPlan(ctx, clients, nil, plan.Transition, plan.Transition.PlanID); err == nil || !strings.Contains(err.Error(), "repair is active") {
		t.Fatalf("ordinary install overlapped repair: %v", err)
	}

	secret, err := clients.Kubernetes.CoreV1().Secrets(plan.Namespace).Get(ctx, plan.StuckRevision.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	secret.Data["release"] = []byte("changed-after-review")
	if _, err = clients.Kubernetes.CoreV1().Secrets(plan.Namespace).Update(ctx, secret, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	runner := func(_ context.Context, name string, arguments ...string) ([]byte, error) {
		if name == "helm" && len(arguments) >= 2 && arguments[0] == "show" && arguments[1] == "crds" {
			return bundle, nil
		}
		return nil, errors.New("unexpected mutation command")
	}
	if err = ApplyInstallRepairPlan(ctx, clients, runner, plan, plan.PlanID); err == nil || !strings.Contains(err.Error(), "changed before repair") {
		t.Fatalf("drifted stuck revision was accepted: %v", err)
	}
	if _, err = clients.Kubernetes.CoreV1().Secrets(plan.Namespace).Get(ctx, plan.StuckRevision.Name, metav1.GetOptions{}); err != nil {
		t.Fatalf("drifted revision was deleted: %v", err)
	}
}

func TestInstallRepairAcceptsFailedStatusButRejectsAnExtraRevision(t *testing.T) {
	ctx := context.Background()
	clients, pendingPlan, _, _, _ := installRepairFixture(t, installCheckpointStaged)
	secret, err := clients.Kubernetes.CoreV1().Secrets(pendingPlan.Namespace).Get(ctx, pendingPlan.StuckRevision.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	secret.Labels["status"] = "failed"
	if _, err = clients.Kubernetes.CoreV1().Secrets(pendingPlan.Namespace).Update(ctx, secret, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	report, err := Preflight(ctx, clients, pendingPlan.Transition.OverlayCIDR)
	if err != nil {
		t.Fatal(err)
	}
	failedPlan, err := BuildInstallRepairPlan(ctx, clients, report, pendingPlan.Namespace, pendingPlan.Release)
	if err != nil || failedPlan.StuckRevision.Status != "failed" {
		t.Fatalf("one exact failed Helm revision was not repairable: %#v %v", failedPlan, err)
	}
	createStuckHelmRevision(t, clients, pendingPlan.Namespace, pendingPlan.Release, 3)
	if _, err = BuildInstallRepairPlan(ctx, clients, report, pendingPlan.Namespace, pendingPlan.Release); err == nil || !strings.Contains(err.Error(), "one newer stuck revision") {
		t.Fatalf("multiple newer Helm revisions were accepted: %v", err)
	}
}

func installRepairFixture(t *testing.T, checkpoint string) (*Clients, InstallRepairPlan, ReleaseManifest, []*apiextensionsv1.CustomResourceDefinition, []byte) {
	t.Helper()
	ctx := context.Background()
	clients := supportedClients(t)
	if _, err := clients.Kubernetes.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "waycloak-system", Labels: map[string]string{"pod-security.kubernetes.io/enforce": "privileged"}}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ensureObservationSecrets(ctx, clients, "waycloak-system", "waycloak", "sha256:"+strings.Repeat("7", 64)); err != nil {
		t.Fatal(err)
	}
	crdObjects, crdIdentities, bundle := testInstallCRDBundle(t)
	sourceManifest := releaseManifest()
	seedInstalledRelease(t, clients, sourceManifest, "waycloak-system", "waycloak", 1, crdObjects)
	source, err := ObserveInstalledRelease(ctx, clients, "waycloak-system", "waycloak")
	if err != nil {
		t.Fatal(err)
	}
	target := sourceManifest
	target.Version = "v1.0.0-beta.2"
	target.ManifestDigest, err = target.IdentityDigest()
	if err != nil {
		t.Fatal(err)
	}
	report, err := Preflight(ctx, clients, "100.96.0.0/16")
	if err != nil {
		t.Fatal(err)
	}
	transition, err := BuildInstallPlan(target, "waycloak-system", "waycloak", "", report, source, crdIdentities, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ensureInstallTransitionJournal(ctx, clients, transition); err != nil {
		t.Fatal(err)
	}
	uid := k8stypes.UID(source.GatewayClassUID)
	if err = clients.Dynamic.Resource(gatewayClassGVR).Delete(ctx, "gluetun.waycloak.io", metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}}); err != nil {
		t.Fatal(err)
	}
	switch checkpoint {
	case installCheckpointClassWithdrawn:
		createStuckHelmRevision(t, clients, transition.Namespace, transition.Release, 2)
	case installCheckpointClassReplaced:
		seedTargetClassOnSourceRuntime(t, clients, source, target, transition.Namespace, transition.Release)
		createStuckHelmRevision(t, clients, transition.Namespace, transition.Release, 2)
	case installCheckpointStaged:
		seedStagedRelease(t, clients, source, target, transition.PlanID, transition.Namespace, transition.Release, 2, crdObjects)
		normalizeStuckHelmRevisions(t, clients, transition.Namespace, transition.Release, 1, 2)
	case installCheckpointTarget:
		seedInstalledRelease(t, clients, target, transition.Namespace, transition.Release, 2, crdObjects)
		normalizeStuckHelmRevisions(t, clients, transition.Namespace, transition.Release, 1, 2)
	default:
		t.Fatalf("unsupported fixture checkpoint %q", checkpoint)
	}
	repair, err := BuildInstallRepairPlan(ctx, clients, report, transition.Namespace, transition.Release)
	if err != nil {
		t.Fatal(err)
	}
	if repair.Checkpoint != checkpoint {
		t.Fatalf("repair checkpoint = %s, want %s", repair.Checkpoint, checkpoint)
	}
	return clients, repair, target, crdObjects, bundle
}

func createStuckHelmRevision(t *testing.T, clients *Clients, namespace, release string, revision int64) {
	t.Helper()
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name: helmRevisionSecretName(release, revision), Namespace: namespace,
		Labels: map[string]string{"owner": "helm", "name": release, "status": "pending-upgrade", "version": strconv.FormatInt(revision, 10)},
	}, Type: corev1.SecretType("helm.sh/release.v1"), Data: map[string][]byte{"release": []byte("opaque-corrupt-helm-payload")}}
	if _, err := clients.Kubernetes.CoreV1().Secrets(namespace).Create(context.Background(), secret, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
}

func normalizeStuckHelmRevisions(t *testing.T, clients *Clients, namespace, release string, sourceRevision, stuckRevision int64) {
	t.Helper()
	for revision, status := range map[int64]string{sourceRevision: "deployed", stuckRevision: "pending-upgrade"} {
		secret, err := clients.Kubernetes.CoreV1().Secrets(namespace).Get(context.Background(), helmRevisionSecretName(release, revision), metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		secret.Labels["status"] = status
		if revision == stuckRevision {
			secret.Data["release"] = []byte("opaque-corrupt-helm-payload")
		}
		if _, err = clients.Kubernetes.CoreV1().Secrets(namespace).Update(context.Background(), secret, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
}

func repairRunner(t *testing.T, clients *Clients, plan InstallRepairPlan, target ReleaseManifest, crds []*apiextensionsv1.CustomResourceDefinition, bundle []byte, calls *int, fail error) func(context.Context, string, ...string) ([]byte, error) {
	t.Helper()
	return func(_ context.Context, name string, arguments ...string) ([]byte, error) {
		if name != "helm" {
			t.Fatalf("unexpected command %s %#v", name, arguments)
		}
		if len(arguments) >= 2 && arguments[0] == "show" && arguments[1] == "crds" {
			return bundle, nil
		}
		assertHelmLifecycleOwnership(t, arguments)
		*calls++
		if fail != nil {
			return nil, fail
		}
		revision := plan.StuckRevision.Version + int64(*calls)
		if (plan.Checkpoint == installCheckpointClassWithdrawn || plan.Checkpoint == installCheckpointClassReplaced) && *calls == 1 {
			seedStagedRelease(t, clients, plan.Transition.Source, target, plan.Transition.PlanID, plan.Namespace, plan.Release, revision, crds)
		} else {
			seedInstalledRelease(t, clients, target, plan.Namespace, plan.Release, revision, crds)
		}
		return nil, nil
	}
}

func seedTargetClassOnSourceRuntime(t *testing.T, clients *Clients, source InstalledReleaseObservation, target ReleaseManifest, namespace, release string) {
	t.Helper()
	class := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "networking.waycloak.io/v1beta1", "kind": "VPNGatewayClass",
		"metadata": map[string]any{"name": "gluetun.waycloak.io"},
		"spec": map[string]any{"releaseIdentity": map[string]any{
			"version": target.Version, "manifestDigest": target.ManifestDigest,
		}},
	}}
	class.SetUID(k8stypes.UID("test-gateway-class-partial-" + release))
	class.SetGeneration(1)
	if _, err := clients.Dynamic.Resource(gatewayClassGVR).Create(context.Background(), class, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	components, err := observeDeployedReleaseComponents(context.Background(), clients, namespace, release)
	if err != nil || !exactClassReplacedComponents(components, source, target) {
		t.Fatalf("fixture did not reproduce target-class/source-runtime checkpoint: %#v %v", components, err)
	}
}

func assertHelmLifecycleOwnership(t *testing.T, arguments []string) {
	t.Helper()
	if !containsString(arguments, "--server-side=true") || !containsString(arguments, "--force-conflicts") || containsString(arguments, "--take-ownership") {
		t.Fatalf("lifecycle Helm mutation lacks narrow server-side field takeover: %#v", arguments)
	}
}

func encodeTestJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
