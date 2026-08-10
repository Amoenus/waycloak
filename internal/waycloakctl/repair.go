// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package waycloakctl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
)

const (
	installRepairSequence = "ExactHelmTransitionRepair-v1"
	installRepairPlanKey  = "install.waycloak.io/repair-plan-id"
	installRepairData     = "repair.json"
	installRepairMaxBytes = 768 << 10
)

type HelmRevisionObservation struct {
	Name         string `json:"name"`
	UID          string `json:"uid"`
	Version      int64  `json:"version"`
	Status       string `json:"status"`
	Type         string `json:"type"`
	ObjectDigest string `json:"objectDigest"`
}

type InstallRepairPlan struct {
	APIVersion      string                  `json:"apiVersion"`
	Kind            string                  `json:"kind"`
	PlanID          string                  `json:"planID"`
	RepairSequence  string                  `json:"repairSequence"`
	Namespace       string                  `json:"namespace"`
	Release         string                  `json:"release"`
	PreflightDigest string                  `json:"preflightDigest"`
	Transition      InstallPlan             `json:"transition"`
	SourceRevision  HelmRevisionObservation `json:"sourceRevision"`
	StuckRevision   HelmRevisionObservation `json:"stuckRevision"`
	Checkpoint      string                  `json:"checkpoint"`
	Commands        []string                `json:"commands"`
	Security        []string                `json:"securityChanges"`
}

type installRepairLiveState struct {
	Checkpoint       string
	CandidatePresent bool
	TargetDeployed   bool
}

func BuildInstallRepairPlan(ctx context.Context, clients *Clients, report PreflightReport, namespace, release string) (InstallRepairPlan, error) {
	transition, _, found, err := loadInstallTransitionJournal(ctx, clients, namespace, release)
	if err != nil {
		return InstallRepairPlan{}, err
	}
	if !found {
		return InstallRepairPlan{}, errors.New("helm transition repair requires the immutable exact-transition journal")
	}
	if !report.Compatible || report.ObservationDigest != transition.PreflightDigest {
		return InstallRepairPlan{}, errors.New("helm transition repair requires the original compatible preflight observation")
	}
	source, stuck, checkpoint, err := observeInitialInstallRepairState(ctx, clients, transition)
	if err != nil {
		return InstallRepairPlan{}, err
	}
	plan := InstallRepairPlan{
		APIVersion: OutputAPIVersion, Kind: "InstallRepairPlan", RepairSequence: installRepairSequence,
		Namespace: namespace, Release: release, PreflightDigest: report.ObservationDigest, Transition: transition,
		SourceRevision: source, StuckRevision: stuck, Checkpoint: checkpoint,
		Commands: []string{"waycloakctl install repair apply --plan <reviewed-repair.json> --confirm <exact-planID>"},
		Security: installRepairSecurity(),
	}
	plan.PlanID = installRepairPlanIdentity(plan)
	plan.Commands[0] = "waycloakctl install repair apply --plan <reviewed-repair.json> --confirm " + plan.PlanID
	return plan, nil
}

func installRepairPlanIdentity(plan InstallRepairPlan) string {
	payload := struct {
		RepairSequence  string                  `json:"repairSequence"`
		Namespace       string                  `json:"namespace"`
		Release         string                  `json:"release"`
		PreflightDigest string                  `json:"preflightDigest"`
		Transition      InstallPlan             `json:"transition"`
		SourceRevision  HelmRevisionObservation `json:"sourceRevision"`
		StuckRevision   HelmRevisionObservation `json:"stuckRevision"`
		Checkpoint      string                  `json:"checkpoint"`
	}{plan.RepairSequence, plan.Namespace, plan.Release, plan.PreflightDigest, plan.Transition, plan.SourceRevision, plan.StuckRevision, plan.Checkpoint}
	data, _ := json.Marshal(payload)
	return digestBytes(data)
}

func (plan InstallRepairPlan) validate() error {
	if plan.APIVersion != OutputAPIVersion || plan.Kind != "InstallRepairPlan" || plan.RepairSequence != installRepairSequence ||
		!validDigest(plan.PlanID) || plan.Namespace == "" || plan.Release == "" || !validDigest(plan.PreflightDigest) ||
		plan.Checkpoint != installCheckpointClassWithdrawn && plan.Checkpoint != installCheckpointClassReplaced && plan.Checkpoint != installCheckpointStaged && plan.Checkpoint != installCheckpointTarget {
		return errors.New("helm transition repair plan identity is incomplete")
	}
	if err := plan.Transition.validate(); err != nil || plan.Transition.Operation != installOperationTransition || plan.Transition.Namespace != plan.Namespace || plan.Transition.Release != plan.Release || plan.Transition.PreflightDigest != plan.PreflightDigest {
		return errors.New("helm transition repair does not bind one exact transition")
	}
	if err := validateHelmRevisionObservation(plan.SourceRevision, plan.Release); err != nil || plan.SourceRevision.Status != "deployed" || plan.SourceRevision.Version != plan.Transition.Source.HelmRevision {
		return errors.New("helm transition repair source revision is invalid")
	}
	if err := validateHelmRevisionObservation(plan.StuckRevision, plan.Release); err != nil || plan.StuckRevision.Version <= plan.SourceRevision.Version || plan.StuckRevision.Status != "pending-upgrade" && plan.StuckRevision.Status != "failed" {
		return errors.New("helm transition repair stuck revision is invalid")
	}
	if plan.PlanID != installRepairPlanIdentity(plan) {
		return errors.New("helm transition repair plan content does not match planID")
	}
	expectedCommands := []string{"waycloakctl install repair apply --plan <reviewed-repair.json> --confirm " + plan.PlanID}
	if !reflect.DeepEqual(plan.Commands, expectedCommands) || !reflect.DeepEqual(plan.Security, installRepairSecurity()) {
		return errors.New("helm transition repair plan instructions are not canonical")
	}
	return nil
}

func installRepairSecurity() []string {
	return []string{"persist an immutable repair journal before deleting one exact stuck Helm revision", "retain the CNI deny path and resume only the original exact transition checkpoint", "never rewrite Helm storage or invoke an unverified rollback"}
}

func validateHelmRevisionObservation(observation HelmRevisionObservation, release string) error {
	if observation.Name != helmRevisionSecretName(release, observation.Version) || observation.UID == "" || observation.Version < 1 || observation.Type != "helm.sh/release.v1" || !validDigest(observation.ObjectDigest) {
		return errors.New("helm revision observation is incomplete")
	}
	return nil
}

func EncodeInstallRepairPlan(plan InstallRepairPlan) ([]byte, error) {
	data, err := json.MarshalIndent(plan, "", "  ")
	return append(data, '\n'), err
}

func LoadInstallRepairPlan(path string) (InstallRepairPlan, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return InstallRepairPlan{}, err
	}
	return decodeInstallRepairPlan(data)
}

func decodeInstallRepairPlan(data []byte) (InstallRepairPlan, error) {
	if len(data) > installRepairMaxBytes {
		return InstallRepairPlan{}, errors.New("helm transition repair plan exceeds size limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var plan InstallRepairPlan
	if err := decoder.Decode(&plan); err != nil {
		return plan, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return plan, errors.New("helm transition repair plan contains trailing JSON")
	}
	return plan, plan.validate()
}

func installRepairJournalName(release string) string {
	return chartFullname(release) + "-release-repair"
}

func ensureNoInstallRepair(ctx context.Context, clients *Clients, namespace, release string) error {
	_, _, found, err := loadInstallRepairJournal(ctx, clients, namespace, release)
	if err != nil {
		return err
	}
	if found {
		return errors.New("an exact Helm transition repair is active; resume it before changing the release")
	}
	return nil
}

func loadInstallRepairJournal(ctx context.Context, clients *Clients, namespace, release string) (InstallRepairPlan, *corev1.ConfigMap, bool, error) {
	object, err := clients.Kubernetes.CoreV1().ConfigMaps(namespace).Get(ctx, installRepairJournalName(release), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return InstallRepairPlan{}, nil, false, nil
	}
	if err != nil {
		return InstallRepairPlan{}, nil, false, fmt.Errorf("read active Helm transition repair: %w", err)
	}
	if object.Annotations[installReleaseOwnerKey] != release || !validDigest(object.Annotations[installRepairPlanKey]) || object.Immutable == nil || !*object.Immutable || len(object.Data) != 1 {
		return InstallRepairPlan{}, object, true, errors.New("active Helm transition repair journal is foreign or malformed")
	}
	encoded, ok := object.Data[installRepairData]
	if !ok || len(encoded) > installRepairMaxBytes {
		return InstallRepairPlan{}, object, true, errors.New("active Helm transition repair journal lacks one bounded reviewed plan")
	}
	plan, err := decodeInstallRepairPlan([]byte(encoded))
	if err != nil {
		return InstallRepairPlan{}, object, true, fmt.Errorf("decode active Helm transition repair journal: %w", err)
	}
	if plan.Namespace != namespace || plan.Release != release || plan.PlanID != object.Annotations[installRepairPlanKey] {
		return InstallRepairPlan{}, object, true, errors.New("active Helm transition repair journal identity is inconsistent")
	}
	return plan, object, true, nil
}

func ensureInstallRepairJournal(ctx context.Context, clients *Clients, plan InstallRepairPlan) (*corev1.ConfigMap, error) {
	encoded, err := EncodeInstallRepairPlan(plan)
	if err != nil {
		return nil, err
	}
	if len(encoded) > installRepairMaxBytes {
		return nil, errors.New("helm transition repair plan exceeds journal size limit")
	}
	immutable := true
	object := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
		Name: installRepairJournalName(plan.Release), Namespace: plan.Namespace,
		Annotations: map[string]string{installReleaseOwnerKey: plan.Release, installRepairPlanKey: plan.PlanID},
		Labels:      map[string]string{"app.kubernetes.io/managed-by": "waycloakctl", "app.kubernetes.io/component": "helm-transition-repair"},
	}, Immutable: &immutable, Data: map[string]string{installRepairData: string(encoded)}}
	created, err := clients.Kubernetes.CoreV1().ConfigMaps(plan.Namespace).Create(ctx, object, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		active, existing, found, loadErr := loadInstallRepairJournal(ctx, clients, plan.Namespace, plan.Release)
		if loadErr != nil {
			return nil, loadErr
		}
		if !found || !reflect.DeepEqual(active, plan) {
			return nil, errors.New("another Helm transition repair is already active")
		}
		return existing, nil
	}
	if err != nil {
		return nil, fmt.Errorf("create Helm transition repair journal: %w", err)
	}
	return created, nil
}

func deleteInstallRepairJournal(ctx context.Context, clients *Clients, plan InstallRepairPlan) error {
	active, object, found, err := loadInstallRepairJournal(ctx, clients, plan.Namespace, plan.Release)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	if !reflect.DeepEqual(active, plan) {
		return errors.New("refusing to remove a different Helm transition repair journal")
	}
	uid := object.UID
	return clients.Kubernetes.CoreV1().ConfigMaps(plan.Namespace).Delete(ctx, object.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}})
}

func recoverInstallRepairPlan(ctx context.Context, clients *Clients, namespace, release string, report PreflightReport) (InstallRepairPlan, bool, error) {
	plan, _, found, err := loadInstallRepairJournal(ctx, clients, namespace, release)
	if err != nil || !found {
		return InstallRepairPlan{}, found, err
	}
	if !report.Compatible || report.ObservationDigest != plan.PreflightDigest {
		return InstallRepairPlan{}, true, errors.New("active Helm transition repair no longer matches the reviewed preflight")
	}
	if _, err := observeInstallRepairLiveState(ctx, clients, plan); err != nil {
		return InstallRepairPlan{}, true, fmt.Errorf("active Helm transition repair is not recoverable: %w", err)
	}
	return plan, true, nil
}

func observeInitialInstallRepairState(ctx context.Context, clients *Clients, transition InstallPlan) (HelmRevisionObservation, HelmRevisionObservation, string, error) {
	records, err := listHelmRevisionObservations(ctx, clients, transition.Namespace, transition.Release)
	if err != nil {
		return HelmRevisionObservation{}, HelmRevisionObservation{}, "", err
	}
	var source HelmRevisionObservation
	newer := make([]HelmRevisionObservation, 0, 1)
	for _, record := range records {
		if record.Version == transition.Source.HelmRevision && record.Status == "deployed" {
			if source.Name != "" {
				return source, HelmRevisionObservation{}, "", errors.New("helm repair source has multiple deployed records")
			}
			source = record
		}
		if record.Version > transition.Source.HelmRevision {
			newer = append(newer, record)
		}
	}
	if source.Name == "" || len(newer) != 1 {
		return source, HelmRevisionObservation{}, "", errors.New("helm repair requires one exact source and one newer stuck revision")
	}
	stuck := newer[0]
	if stuck.Status != "pending-upgrade" && stuck.Status != "failed" {
		return source, stuck, "", errors.New("newer Helm revision is not a supported pending-upgrade or failed transition")
	}
	components, err := observeDeployedReleaseComponents(ctx, clients, transition.Namespace, transition.Release)
	if err != nil {
		return source, stuck, "", err
	}
	checkpoint, err := classifyInstallRepairCheckpoint(components, transition, stuck.Version)
	return source, stuck, checkpoint, err
}

func observeInstallRepairLiveState(ctx context.Context, clients *Clients, plan InstallRepairPlan) (installRepairLiveState, error) {
	records, err := listHelmRevisionObservations(ctx, clients, plan.Namespace, plan.Release)
	if err != nil {
		return installRepairLiveState{}, err
	}
	var source *HelmRevisionObservation
	var candidate *HelmRevisionObservation
	newer := make([]HelmRevisionObservation, 0, 1)
	for index := range records {
		record := &records[index]
		if record.Name == plan.SourceRevision.Name {
			source = record
		}
		if record.Name == plan.StuckRevision.Name {
			candidate = record
		}
		if record.Version > plan.SourceRevision.Version {
			newer = append(newer, *record)
		}
	}
	if candidate != nil {
		if !reflect.DeepEqual(*candidate, plan.StuckRevision) || source == nil || !reflect.DeepEqual(*source, plan.SourceRevision) || len(newer) != 1 {
			return installRepairLiveState{}, errors.New("journal-bound Helm revisions changed before repair")
		}
		components, err := observeDeployedReleaseComponents(ctx, clients, plan.Namespace, plan.Release)
		if err != nil {
			return installRepairLiveState{}, err
		}
		checkpoint, err := classifyInstallRepairCheckpoint(components, plan.Transition, plan.StuckRevision.Version)
		if err != nil || repairCheckpointRank(checkpoint) < repairCheckpointRank(plan.Checkpoint) {
			return installRepairLiveState{}, errors.New("runtime no longer matches the reviewed Helm repair checkpoint")
		}
		return installRepairLiveState{Checkpoint: checkpoint, CandidatePresent: true}, nil
	}
	if len(newer) > 1 {
		return installRepairLiveState{}, errors.New("multiple newer Helm revisions appeared during repair")
	}
	components, err := observeDeployedReleaseComponents(ctx, clients, plan.Namespace, plan.Release)
	if err != nil {
		return installRepairLiveState{}, err
	}
	effectiveRevision := plan.StuckRevision.Version
	if len(newer) == 1 {
		if newer[0].Status != "deployed" {
			return installRepairLiveState{}, errors.New("a different non-deployed Helm revision appeared during repair")
		}
		effectiveRevision = newer[0].Version
	} else if source == nil || !reflect.DeepEqual(*source, plan.SourceRevision) {
		return installRepairLiveState{}, errors.New("source Helm revision changed after stuck-revision removal")
	}
	checkpoint, err := classifyInstallRepairCheckpoint(components, plan.Transition, effectiveRevision)
	if err != nil || repairCheckpointRank(checkpoint) < repairCheckpointRank(plan.Checkpoint) {
		return installRepairLiveState{}, errors.New("runtime regressed after stuck-revision removal")
	}
	return installRepairLiveState{Checkpoint: checkpoint, TargetDeployed: len(newer) == 1 && checkpoint == installCheckpointTarget}, nil
}

func classifyInstallRepairCheckpoint(components deployedReleaseComponents, transition InstallPlan, effectiveRevision int64) (string, error) {
	if exactSourceComponents(components, transition.Source, true) {
		return installCheckpointClassWithdrawn, nil
	}
	if exactClassReplacedComponents(components, transition.Source, transition.Target) {
		return installCheckpointClassReplaced, nil
	}
	components.HelmRevision = effectiveRevision
	if exactStagedComponents(components, transition.Source, transition.Target, transition.TargetCRDs) {
		return installCheckpointStaged, nil
	}
	if exactTargetComponents(components, transition.Source, transition.Target, transition.TargetCRDs) {
		return installCheckpointTarget, nil
	}
	return "", errors.New("runtime does not match a supported exact Helm repair checkpoint")
}

func exactTargetComponents(components deployedReleaseComponents, source InstalledReleaseObservation, target ReleaseManifest, targetCRDs map[string]string) bool {
	if source.State != installStateDeployed || components.HelmRevision <= source.HelmRevision || !components.ClassPresent || components.ObservationCapabilityHeld ||
		components.ControllerVersion != target.Version || components.ControllerManifest != target.ManifestDigest ||
		components.CNIVersion != target.Version || components.CNIManifest != target.ManifestDigest ||
		components.NodeAgentVersion != target.Version || components.NodeAgentManifest != target.ManifestDigest ||
		components.ObservationRotationID != source.ObservationRotationID || components.ClassVersion != target.Version || components.ClassManifest != target.ManifestDigest ||
		components.ClassUID == "" || components.ClassGeneration < 1 || !sameTransitionTrust(components, source) || !reflect.DeepEqual(components.CRDIdentities, targetCRDs) {
		return false
	}
	for _, name := range installRuntimeImageNames {
		if components.Images[name] != target.Images[name].Repository+"@"+target.Images[name].Digest {
			return false
		}
	}
	return true
}

func repairCheckpointRank(checkpoint string) int {
	switch checkpoint {
	case installCheckpointClassWithdrawn:
		return 1
	case installCheckpointClassReplaced:
		return 2
	case installCheckpointStaged:
		return 3
	case installCheckpointTarget:
		return 4
	default:
		return 0
	}
}

func listHelmRevisionObservations(ctx context.Context, clients *Clients, namespace, release string) ([]HelmRevisionObservation, error) {
	selector := labels.Set{"owner": "helm", "name": release}.AsSelector().String()
	secrets, err := clients.Kubernetes.CoreV1().Secrets(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return nil, fmt.Errorf("list Helm release records: %w", err)
	}
	records := make([]HelmRevisionObservation, 0, len(secrets.Items))
	for index := range secrets.Items {
		record, err := observeHelmRevision(&secrets.Items[index], release)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, nil
}

func observeHelmRevision(secret *corev1.Secret, release string) (HelmRevisionObservation, error) {
	version, err := strconv.ParseInt(secret.Labels["version"], 10, 64)
	if err != nil || version < 1 || secret.Name != helmRevisionSecretName(release, version) || secret.UID == "" || secret.Type != corev1.SecretType("helm.sh/release.v1") {
		return HelmRevisionObservation{}, fmt.Errorf("helm release record %s/%s has invalid immutable metadata", secret.Namespace, secret.Name)
	}
	return HelmRevisionObservation{Name: secret.Name, UID: string(secret.UID), Version: version, Status: secret.Labels["status"], Type: string(secret.Type), ObjectDigest: helmRevisionObjectDigest(secret)}, nil
}

func helmRevisionObjectDigest(secret *corev1.Secret) string {
	payload := struct {
		Name        string            `json:"name"`
		Namespace   string            `json:"namespace"`
		UID         string            `json:"uid"`
		Type        corev1.SecretType `json:"type"`
		Labels      map[string]string `json:"labels"`
		Annotations map[string]string `json:"annotations,omitempty"`
		Immutable   *bool             `json:"immutable,omitempty"`
		Data        map[string][]byte `json:"data,omitempty"`
	}{secret.Name, secret.Namespace, string(secret.UID), secret.Type, secret.Labels, secret.Annotations, secret.Immutable, secret.Data}
	data, _ := json.Marshal(payload)
	return digestBytes(data)
}

func helmRevisionSecretName(release string, version int64) string {
	return "sh.helm.release.v1." + release + ".v" + strconv.FormatInt(version, 10)
}

func ApplyInstallRepairPlan(ctx context.Context, clients *Clients, runner func(context.Context, string, ...string) ([]byte, error), plan InstallRepairPlan, confirmation string) error {
	if err := plan.validate(); err != nil {
		return err
	}
	if confirmation != plan.PlanID {
		return fmt.Errorf("refusing mutation: --confirm must exactly equal %s", plan.PlanID)
	}
	if err := ensureNoCertificateRotation(ctx, clients, plan.Namespace, plan.Release); err != nil {
		return err
	}
	report, err := Preflight(ctx, clients, plan.Transition.OverlayCIDR)
	if err != nil {
		return fmt.Errorf("re-run preflight before Helm repair: %w", err)
	}
	if !report.Compatible || report.ObservationDigest != plan.PreflightDigest {
		return errors.New("refusing Helm repair: cluster preflight changed after review")
	}
	if runner == nil {
		runner = defaultRunner
	}
	targetCRDs, err := ChartCRDIdentities(ctx, runner, plan.Transition.Chart)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(targetCRDs, plan.Transition.TargetCRDs) {
		return errors.New("refusing Helm repair: exact target chart CRD identity changed")
	}
	active, _, found, err := loadInstallRepairJournal(ctx, clients, plan.Namespace, plan.Release)
	if err != nil {
		return err
	}
	if found && !reflect.DeepEqual(active, plan) {
		return errors.New("another Helm transition repair is active")
	}
	if !found {
		rebuilt, buildErr := BuildInstallRepairPlan(ctx, clients, report, plan.Namespace, plan.Release)
		if buildErr != nil || !reflect.DeepEqual(rebuilt, plan) {
			return errors.New("refusing Helm repair: stuck release or runtime changed after plan review")
		}
		if _, err := ensureInstallRepairJournal(ctx, clients, plan); err != nil {
			return err
		}
	}
	state, err := observeInstallRepairLiveState(ctx, clients, plan)
	if err != nil {
		return err
	}
	if state.CandidatePresent {
		uid := types.UID(plan.StuckRevision.UID)
		if err := clients.Kubernetes.CoreV1().Secrets(plan.Namespace).Delete(ctx, plan.StuckRevision.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}}); err != nil {
			return fmt.Errorf("remove exact stuck Helm revision: %w", err)
		}
		state, err = observeInstallRepairLiveState(ctx, clients, plan)
		if err != nil {
			return err
		}
	}
	if state.TargetDeployed {
		if err := deleteInstallTransitionJournal(ctx, clients, plan.Transition, false); err != nil {
			return err
		}
		return deleteInstallRepairJournal(ctx, clients, plan)
	}
	transition, _, transitionFound, err := loadInstallTransitionJournal(ctx, clients, plan.Namespace, plan.Release)
	if err != nil || !transitionFound || !reflect.DeepEqual(transition, plan.Transition) {
		return errors.New("helm repair lost its immutable exact-transition authority")
	}
	if err := applyInstallPlanAtCheckpoint(ctx, clients, runner, plan.Transition, targetCRDs, state.Checkpoint); err != nil {
		return err
	}
	return deleteInstallRepairJournal(ctx, clients, plan)
}
