// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package waycloakctl

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/Amoenus/waycloak/internal/scheduling"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"
)

const (
	certificateRotationSequence = "ObservationCertificateRotation-v1"
	certificateRotationPlanKey  = "install.waycloak.io/certificate-rotation-plan-id"
	certificateRotationData     = "rotation.json"

	rotationPhaseSource             = "Source"
	rotationPhaseOverlapCAOnly      = "OverlapCAOnly"
	rotationPhaseOverlapOldAgent    = "OverlapOldAgent"
	rotationPhaseOverlapNewAgent    = "OverlapNewAgent"
	rotationPhaseServingSwitched    = "ServingSwitched"
	rotationPhasePrunedTLSOnly      = "PrunedTLSOnly"
	rotationPhasePrunedOldAgent     = "PrunedOldAgent"
	rotationPhasePrunedNewAgentHeld = "PrunedNewAgentHeld"
	rotationPhaseTarget             = "Target"
)

type CertificateRotationPlan struct {
	APIVersion       string                      `json:"apiVersion"`
	Kind             string                      `json:"kind"`
	PlanID           string                      `json:"planID"`
	RotationSequence string                      `json:"rotationSequence"`
	Namespace        string                      `json:"namespace"`
	Release          string                      `json:"release"`
	OverlayCIDR      string                      `json:"overlayCIDR"`
	PreflightDigest  string                      `json:"preflightDigest"`
	Source           InstalledReleaseObservation `json:"source"`
	Commands         []string                    `json:"commands"`
	Security         []string                    `json:"securityChanges"`
}

type certificateRotationJournal struct {
	APIVersion          string                  `json:"apiVersion"`
	Kind                string                  `json:"kind"`
	Plan                CertificateRotationPlan `json:"plan"`
	StagedSecretUID     string                  `json:"stagedSecretUID"`
	TargetCADigest      string                  `json:"targetCADigest"`
	TargetServingDigest string                  `json:"targetServingDigest"`
}

func BuildCertificateRotationPlan(report PreflightReport, source InstalledReleaseObservation, namespace, release, overlayCIDR string) (CertificateRotationPlan, error) {
	if !report.Compatible || !validDigest(report.ObservationDigest) || namespace == "" || release == "" || overlayCIDR == "" {
		return CertificateRotationPlan{}, errors.New("certificate rotation requires a compatible preflight and explicit release identity")
	}
	if err := source.validate(); err != nil {
		return CertificateRotationPlan{}, err
	}
	if source.State != installStateDeployed {
		return CertificateRotationPlan{}, errors.New("certificate rotation requires one exact deployed release")
	}
	plan := CertificateRotationPlan{
		APIVersion: OutputAPIVersion, Kind: "CertificateRotationPlan", RotationSequence: certificateRotationSequence,
		Namespace: namespace, Release: release, OverlayCIDR: overlayCIDR, PreflightDigest: report.ObservationDigest, Source: source,
		Commands: []string{"waycloakctl certificate rotation apply --plan <reviewed-plan.json> --confirm <exact-planID>"},
		Security: []string{"stage a new release-owned private serving identity", "publish bounded old-and-new trust overlap", "withdraw Core-ready scheduling before each trust boundary", "delete the prior trust root only after fresh authenticated node capability"},
	}
	plan.PlanID = certificateRotationPlanIdentity(plan)
	plan.Commands[0] = "waycloakctl certificate rotation apply --plan <reviewed-plan.json> --confirm " + plan.PlanID
	return plan, nil
}

func certificateRotationPlanIdentity(plan CertificateRotationPlan) string {
	payload := struct {
		RotationSequence string                      `json:"rotationSequence"`
		Namespace        string                      `json:"namespace"`
		Release          string                      `json:"release"`
		OverlayCIDR      string                      `json:"overlayCIDR"`
		PreflightDigest  string                      `json:"preflightDigest"`
		Source           InstalledReleaseObservation `json:"source"`
	}{plan.RotationSequence, plan.Namespace, plan.Release, plan.OverlayCIDR, plan.PreflightDigest, plan.Source}
	data, _ := json.Marshal(payload)
	return digestBytes(data)
}

func (plan CertificateRotationPlan) validate() error {
	if plan.APIVersion != OutputAPIVersion || plan.Kind != "CertificateRotationPlan" || plan.RotationSequence != certificateRotationSequence ||
		!validDigest(plan.PlanID) || plan.Namespace == "" || plan.Release == "" || plan.OverlayCIDR == "" || !validDigest(plan.PreflightDigest) {
		return errors.New("certificate rotation plan identity is incomplete")
	}
	if err := plan.Source.validate(); err != nil {
		return err
	}
	if plan.Source.State != installStateDeployed || plan.PlanID != certificateRotationPlanIdentity(plan) {
		return errors.New("certificate rotation plan content does not match planID")
	}
	return nil
}

func EncodeCertificateRotationPlan(plan CertificateRotationPlan) ([]byte, error) {
	data, err := json.MarshalIndent(plan, "", "  ")
	return append(data, '\n'), err
}

func LoadCertificateRotationPlan(path string) (CertificateRotationPlan, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return CertificateRotationPlan{}, err
	}
	return decodeCertificateRotationPlan(data)
}

func decodeCertificateRotationPlan(data []byte) (CertificateRotationPlan, error) {
	if len(data) > 2<<20 {
		return CertificateRotationPlan{}, errors.New("certificate rotation plan exceeds size limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var plan CertificateRotationPlan
	if err := decoder.Decode(&plan); err != nil {
		return plan, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return plan, errors.New("certificate rotation plan contains trailing JSON")
	}
	return plan, plan.validate()
}

func certificateRotationJournalName(release string) string {
	return chartFullname(release) + "-certificate-rotation"
}

func certificateRotationStagedSecretName(release string) string {
	return chartFullname(release) + "-observation-next"
}

func ensureNoCertificateRotation(ctx context.Context, clients *Clients, namespace, release string) error {
	_, _, found, err := loadCertificateRotationJournal(ctx, clients, namespace, release)
	if err != nil {
		return err
	}
	if found {
		return errors.New("an observation certificate rotation is active; resume it before changing the release")
	}
	staged, err := clients.Kubernetes.CoreV1().Secrets(namespace).Get(ctx, certificateRotationStagedSecretName(release), metav1.GetOptions{})
	if err == nil {
		return fmt.Errorf("orphaned staged certificate Secret %s/%s requires the reviewed rotation plan or explicit repair", namespace, staged.Name)
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

func validateNoOrMatchingStagedCertificate(ctx context.Context, clients *Clients, plan CertificateRotationPlan) error {
	staged, err := clients.Kubernetes.CoreV1().Secrets(plan.Namespace).Get(ctx, certificateRotationStagedSecretName(plan.Release), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if staged.Annotations[installReleaseOwnerKey] != plan.Release || staged.Annotations[certificateRotationPlanKey] != plan.PlanID || staged.Immutable == nil || !*staged.Immutable {
		return errors.New("orphaned staged certificate does not match the reproducible reviewed plan")
	}
	if err := validateInstallSecret(staged, corev1.SecretTypeTLS); err != nil {
		return fmt.Errorf("orphaned staged certificate is invalid: %w", err)
	}
	return nil
}

func ensureNoInstallTransition(ctx context.Context, clients *Clients, namespace, release string) error {
	if err := ensureNoInstallRepair(ctx, clients, namespace, release); err != nil {
		return err
	}
	_, _, found, err := loadInstallTransitionJournal(ctx, clients, namespace, release)
	if err != nil {
		return err
	}
	if found {
		return errors.New("an exact release transition is active; resume it before rotating observation trust")
	}
	return nil
}

func ensureStagedCertificate(ctx context.Context, clients *Clients, plan CertificateRotationPlan) (*corev1.Secret, error) {
	secrets := clients.Kubernetes.CoreV1().Secrets(plan.Namespace)
	name := certificateRotationStagedSecretName(plan.Release)
	secret, err := secrets.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		caPEM, certPEM, keyPEM, generateErr := observationIdentity(plan.Release, plan.Namespace)
		if generateErr != nil {
			return nil, generateErr
		}
		immutable := true
		secret = &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: plan.Namespace,
			Annotations: map[string]string{installReleaseOwnerKey: plan.Release, certificateRotationPlanKey: plan.PlanID},
			Labels:      map[string]string{"app.kubernetes.io/managed-by": "waycloakctl", "app.kubernetes.io/component": "observation-certificate-rotation"},
		}, Immutable: &immutable, Type: corev1.SecretTypeTLS, Data: map[string][]byte{"ca.crt": caPEM, "tls.crt": certPEM, "tls.key": keyPEM}}
		secret, err = secrets.Create(ctx, secret, metav1.CreateOptions{})
	}
	if err != nil {
		return nil, fmt.Errorf("stage observation certificate: %w", err)
	}
	if secret.Annotations[installReleaseOwnerKey] != plan.Release || secret.Annotations[certificateRotationPlanKey] != plan.PlanID || secret.Immutable == nil || !*secret.Immutable {
		return nil, errors.New("staged observation certificate is foreign or mutable")
	}
	if err := validateInstallSecret(secret, corev1.SecretTypeTLS); err != nil {
		return nil, fmt.Errorf("validate staged observation certificate: %w", err)
	}
	if secret.UID == "" {
		return nil, errors.New("staged observation certificate lacks a durable Kubernetes UID")
	}
	return secret, nil
}

func newCertificateRotationJournal(plan CertificateRotationPlan, staged *corev1.Secret) certificateRotationJournal {
	return certificateRotationJournal{
		APIVersion: OutputAPIVersion, Kind: "CertificateRotationJournal", Plan: plan, StagedSecretUID: string(staged.UID),
		TargetCADigest: digestBytes(staged.Data["ca.crt"]), TargetServingDigest: digestBytes(staged.Data["tls.crt"]),
	}
}

func (journal certificateRotationJournal) validate() error {
	if journal.APIVersion != OutputAPIVersion || journal.Kind != "CertificateRotationJournal" || journal.StagedSecretUID == "" || !validDigest(journal.TargetCADigest) || !validDigest(journal.TargetServingDigest) {
		return errors.New("certificate rotation journal identity is incomplete")
	}
	return journal.Plan.validate()
}

func ensureCertificateRotationJournal(ctx context.Context, clients *Clients, plan CertificateRotationPlan, staged *corev1.Secret) (certificateRotationJournal, *corev1.ConfigMap, error) {
	wanted := newCertificateRotationJournal(plan, staged)
	encoded, err := json.Marshal(wanted)
	if err != nil {
		return wanted, nil, err
	}
	immutable := true
	name := certificateRotationJournalName(plan.Release)
	object := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
		Name: name, Namespace: plan.Namespace,
		Annotations: map[string]string{installReleaseOwnerKey: plan.Release, certificateRotationPlanKey: plan.PlanID},
		Labels:      map[string]string{"app.kubernetes.io/managed-by": "waycloakctl", "app.kubernetes.io/component": "observation-certificate-rotation"},
	}, Immutable: &immutable, Data: map[string]string{certificateRotationData: string(encoded)}}
	created, err := clients.Kubernetes.CoreV1().ConfigMaps(plan.Namespace).Create(ctx, object, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		active, existing, found, loadErr := loadCertificateRotationJournal(ctx, clients, plan.Namespace, plan.Release)
		if loadErr != nil {
			return wanted, nil, loadErr
		}
		if !found || !reflect.DeepEqual(active, wanted) {
			return wanted, nil, errors.New("another observation certificate rotation is already active")
		}
		return active, existing, nil
	}
	if err != nil {
		return wanted, nil, fmt.Errorf("create observation certificate rotation journal: %w", err)
	}
	return wanted, created, nil
}

func loadCertificateRotationJournal(ctx context.Context, clients *Clients, namespace, release string) (certificateRotationJournal, *corev1.ConfigMap, bool, error) {
	object, err := clients.Kubernetes.CoreV1().ConfigMaps(namespace).Get(ctx, certificateRotationJournalName(release), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return certificateRotationJournal{}, nil, false, nil
	}
	if err != nil {
		return certificateRotationJournal{}, nil, false, fmt.Errorf("read observation certificate rotation journal: %w", err)
	}
	if object.Annotations[installReleaseOwnerKey] != release || !validDigest(object.Annotations[certificateRotationPlanKey]) || object.Immutable == nil || !*object.Immutable || len(object.Data) != 1 {
		return certificateRotationJournal{}, object, true, errors.New("observation certificate rotation journal is foreign or malformed")
	}
	encoded, ok := object.Data[certificateRotationData]
	if !ok || len(encoded) > 512<<10 {
		return certificateRotationJournal{}, object, true, errors.New("observation certificate rotation journal lacks bounded state")
	}
	decoder := json.NewDecoder(strings.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var journal certificateRotationJournal
	if err := decoder.Decode(&journal); err != nil {
		return journal, object, true, fmt.Errorf("decode observation certificate rotation journal: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return journal, object, true, errors.New("observation certificate rotation journal contains trailing JSON")
	}
	if err := journal.validate(); err != nil || journal.Plan.Namespace != namespace || journal.Plan.Release != release || journal.Plan.PlanID != object.Annotations[certificateRotationPlanKey] {
		return journal, object, true, errors.New("observation certificate rotation journal identity is inconsistent")
	}
	return journal, object, true, nil
}

func validateStagedCertificate(journal certificateRotationJournal, staged *corev1.Secret) error {
	if staged == nil || string(staged.UID) != journal.StagedSecretUID || staged.Annotations[installReleaseOwnerKey] != journal.Plan.Release || staged.Annotations[certificateRotationPlanKey] != journal.Plan.PlanID || staged.Immutable == nil || !*staged.Immutable {
		return errors.New("staged observation certificate no longer matches its immutable journal")
	}
	if err := validateInstallSecret(staged, corev1.SecretTypeTLS); err != nil {
		return err
	}
	if digestBytes(staged.Data["ca.crt"]) != journal.TargetCADigest || digestBytes(staged.Data["tls.crt"]) != journal.TargetServingDigest {
		return errors.New("staged observation certificate public identity changed after journaling")
	}
	return nil
}

func recoverCertificateRotationPlan(ctx context.Context, clients *Clients, namespace, release string, report PreflightReport) (CertificateRotationPlan, bool, error) {
	journal, _, found, err := loadCertificateRotationJournal(ctx, clients, namespace, release)
	if err != nil || !found {
		return CertificateRotationPlan{}, found, err
	}
	if !report.Compatible || report.ObservationDigest != journal.Plan.PreflightDigest {
		return CertificateRotationPlan{}, true, errors.New("active certificate rotation no longer matches the reviewed cluster preflight")
	}
	staged, err := clients.Kubernetes.CoreV1().Secrets(namespace).Get(ctx, certificateRotationStagedSecretName(release), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if completed, completeErr := completedRotationWithoutStaged(ctx, clients, journal); completeErr != nil || !completed {
			if completeErr != nil {
				return CertificateRotationPlan{}, true, completeErr
			}
			return CertificateRotationPlan{}, true, errors.New("active certificate rotation lost staged private material before reaching its exact target")
		}
		return journal.Plan, true, nil
	}
	if err != nil {
		return CertificateRotationPlan{}, true, fmt.Errorf("read active staged certificate: %w", err)
	}
	if err := validateStagedCertificate(journal, staged); err != nil {
		return CertificateRotationPlan{}, true, err
	}
	if _, err := observeCertificateRotationPhase(ctx, clients, journal, staged); err != nil {
		return CertificateRotationPlan{}, true, fmt.Errorf("active certificate rotation is not recoverable: %w", err)
	}
	return journal.Plan, true, nil
}

func ApplyCertificateRotationPlan(ctx context.Context, clients *Clients, plan CertificateRotationPlan, confirmation string) error {
	if err := plan.validate(); err != nil {
		return err
	}
	if confirmation != plan.PlanID {
		return fmt.Errorf("refusing mutation: --confirm must exactly equal %s", plan.PlanID)
	}
	if err := ensureNoInstallTransition(ctx, clients, plan.Namespace, plan.Release); err != nil {
		return err
	}
	report, err := Preflight(ctx, clients, plan.OverlayCIDR)
	if err != nil {
		return fmt.Errorf("re-run preflight before certificate mutation: %w", err)
	}
	if !report.Compatible || report.ObservationDigest != plan.PreflightDigest {
		return errors.New("refusing certificate mutation: cluster preflight changed after plan review")
	}

	journal, _, found, err := loadCertificateRotationJournal(ctx, clients, plan.Namespace, plan.Release)
	if err != nil {
		return err
	}
	if found && !reflect.DeepEqual(journal.Plan, plan) {
		return errors.New("another observation certificate rotation is already active")
	}
	if !found {
		current, observeErr := ObserveInstalledRelease(ctx, clients, plan.Namespace, plan.Release)
		if observeErr != nil || current.ObservationDigest != plan.Source.ObservationDigest {
			return errors.New("refusing certificate mutation: deployed release changed after plan review")
		}
		caSecret, getErr := getRotationSecret(ctx, clients, plan.Namespace, plan.Release+"-observation-ca")
		if getErr != nil {
			return getErr
		}
		oldCAs, parseErr := parseCABundle(caSecret.Data["ca.crt"])
		if parseErr != nil || len(oldCAs) != 1 {
			return errors.New("certificate rotation source must contain exactly one valid CA")
		}
	}
	var staged *corev1.Secret
	if found {
		staged, err = clients.Kubernetes.CoreV1().Secrets(plan.Namespace).Get(ctx, certificateRotationStagedSecretName(plan.Release), metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			if completed, completeErr := completedRotationWithoutStaged(ctx, clients, journal); completeErr != nil {
				return completeErr
			} else if completed {
				return deleteCertificateRotationJournal(ctx, clients, journal)
			}
			return errors.New("active certificate rotation lost staged private material before reaching its exact target")
		}
		if err != nil {
			return err
		}
	} else {
		staged, err = ensureStagedCertificate(ctx, clients, plan)
		if err != nil {
			return err
		}
		journal, _, err = ensureCertificateRotationJournal(ctx, clients, plan, staged)
		if err != nil {
			return err
		}
	}
	if err := validateStagedCertificate(journal, staged); err != nil {
		return err
	}

	for attempts := 0; attempts < 12; attempts++ {
		phase, err := observeCertificateRotationPhase(ctx, clients, journal, staged)
		if err != nil {
			return fmt.Errorf("refusing certificate rotation: %w", err)
		}
		switch phase {
		case rotationPhaseSource:
			sourceCA, err := rotationSourceCA(ctx, clients, plan, staged)
			if err != nil {
				return err
			}
			if err := updateRotationCASecret(ctx, clients, plan, appendPEMBundle(sourceCA, staged.Data["ca.crt"])); err != nil {
				return err
			}
		case rotationPhaseOverlapCAOnly:
			sourceCA, err := rotationSourceCA(ctx, clients, plan, staged)
			if err != nil {
				return err
			}
			if err := updateRotationTLSSecret(ctx, clients, plan, appendPEMBundle(sourceCA, staged.Data["ca.crt"]), nil); err != nil {
				return err
			}
		case rotationPhaseOverlapOldAgent:
			if err := withdrawRotationNodes(ctx, clients, plan); err != nil {
				return err
			}
			observedAfter := time.Now().UTC()
			if err := rollRotationNodeAgent(ctx, clients, plan, plan.PlanID+"-overlap", true); err != nil {
				return err
			}
			if err := waitRotationNodesObserved(ctx, clients, plan, observedAfter); err != nil {
				return err
			}
		case rotationPhaseOverlapNewAgent:
			if err := withdrawRotationNodes(ctx, clients, plan); err != nil {
				return err
			}
			sourceCA, err := rotationSourceCA(ctx, clients, plan, staged)
			if err != nil {
				return err
			}
			if err := updateRotationTLSSecret(ctx, clients, plan, appendPEMBundle(sourceCA, staged.Data["ca.crt"]), staged); err != nil {
				return err
			}
		case rotationPhaseServingSwitched:
			if err := waitRotationNodesObserved(ctx, clients, plan, time.Now().UTC()); err != nil {
				return err
			}
			if err := updateRotationTLSSecret(ctx, clients, plan, staged.Data["ca.crt"], staged); err != nil {
				return err
			}
		case rotationPhasePrunedTLSOnly:
			if err := updateRotationCASecret(ctx, clients, plan, staged.Data["ca.crt"]); err != nil {
				return err
			}
		case rotationPhasePrunedOldAgent:
			observedAfter := time.Now().UTC()
			if err := rollRotationNodeAgent(ctx, clients, plan, plan.PlanID+"-new-only", true); err != nil {
				return err
			}
			if err := waitRotationNodesObserved(ctx, clients, plan, observedAfter); err != nil {
				return err
			}
		case rotationPhasePrunedNewAgentHeld:
			if err := rollRotationNodeAgent(ctx, clients, plan, plan.PlanID, false); err != nil {
				return err
			}
			if err := waitRotationNodesReady(ctx, clients, plan, time.Now().UTC()); err != nil {
				return err
			}
		case rotationPhaseTarget:
			if err := waitRotationNodesReady(ctx, clients, plan, time.Now().UTC()); err != nil {
				return err
			}
			return completeCertificateRotation(ctx, clients, journal)
		default:
			return errors.New("unknown certificate rotation phase")
		}
	}
	return errors.New("certificate rotation exceeded its bounded phase count")
}

func observeCertificateRotationPhase(ctx context.Context, clients *Clients, journal certificateRotationJournal, staged *corev1.Secret) (string, error) {
	plan := journal.Plan
	components, err := observeDeployedReleaseComponents(ctx, clients, plan.Namespace, plan.Release)
	if err != nil {
		return "", err
	}
	if !sameRotationRelease(components, plan.Source) {
		return "", errors.New("runtime, CRD, class, Helm revision, or stable Secret UID changed during certificate rotation")
	}
	caSecret, err := getRotationSecret(ctx, clients, plan.Namespace, plan.Release+"-observation-ca")
	if err != nil {
		return "", err
	}
	tlsSecret, err := getRotationSecret(ctx, clients, plan.Namespace, plan.Release+"-observation-tls")
	if err != nil {
		return "", err
	}
	caData, tlsCA, serving := caSecret.Data["ca.crt"], tlsSecret.Data["ca.crt"], tlsSecret.Data["tls.crt"]
	oldServing := digestBytes(serving) == plan.Source.ObservationServingDigest
	newServing := digestBytes(serving) == journal.TargetServingDigest && bytes.Equal(tlsSecret.Data["tls.key"], staged.Data["tls.key"])
	rotationID := components.ObservationRotationID
	if bytes.Equal(caData, staged.Data["ca.crt"]) && bytes.Equal(tlsCA, staged.Data["ca.crt"]) && newServing {
		switch rotationID {
		case plan.PlanID + "-overlap":
			if !components.ObservationCapabilityHeld {
				return "", errors.New("new-only trust lost its fail-closed capability hold")
			}
			return rotationPhasePrunedOldAgent, nil
		case plan.PlanID + "-new-only":
			if !components.ObservationCapabilityHeld {
				return "", errors.New("new-only agent checkpoint lost its capability hold")
			}
			return rotationPhasePrunedNewAgentHeld, nil
		case plan.PlanID:
			if components.ObservationCapabilityHeld {
				return "", errors.New("completed trust retains a capability hold")
			}
			return rotationPhaseTarget, nil
		default:
			return "", errors.New("new-only trust has an unrecognized node-agent rotation identity")
		}
	}
	sourceCA := sourceCAForPlan(plan, caSecret, tlsSecret, staged)
	if sourceCA == nil {
		return "", errors.New("cannot recover the reviewed source CA from the exact rotation state")
	}
	overlap := appendPEMBundle(sourceCA, staged.Data["ca.crt"])
	switch {
	case bytes.Equal(caData, sourceCA) && bytes.Equal(tlsCA, sourceCA) && oldServing && rotationID == plan.Source.ObservationRotationID && !components.ObservationCapabilityHeld:
		return rotationPhaseSource, nil
	case bytes.Equal(caData, overlap) && bytes.Equal(tlsCA, sourceCA) && oldServing && rotationID == plan.Source.ObservationRotationID && !components.ObservationCapabilityHeld:
		return rotationPhaseOverlapCAOnly, nil
	case bytes.Equal(caData, overlap) && bytes.Equal(tlsCA, overlap) && oldServing && rotationID == plan.Source.ObservationRotationID && !components.ObservationCapabilityHeld:
		return rotationPhaseOverlapOldAgent, nil
	case bytes.Equal(caData, overlap) && bytes.Equal(tlsCA, overlap) && oldServing && rotationID == plan.PlanID+"-overlap" && components.ObservationCapabilityHeld:
		return rotationPhaseOverlapNewAgent, nil
	case bytes.Equal(caData, overlap) && bytes.Equal(tlsCA, overlap) && newServing && rotationID == plan.PlanID+"-overlap" && components.ObservationCapabilityHeld:
		return rotationPhaseServingSwitched, nil
	case bytes.Equal(caData, overlap) && bytes.Equal(tlsCA, staged.Data["ca.crt"]) && newServing && rotationID == plan.PlanID+"-overlap" && components.ObservationCapabilityHeld:
		return rotationPhasePrunedTLSOnly, nil
	default:
		return "", errors.New("certificate material does not match a journal-bound exact rotation checkpoint")
	}
}

func sameRotationRelease(components deployedReleaseComponents, source InstalledReleaseObservation) bool {
	return components.HelmRevision == source.HelmRevision && components.ControllerVersion == source.Version && components.ControllerManifest == source.ManifestDigest &&
		components.CNIVersion == source.Version && components.CNIManifest == source.ManifestDigest && components.NodeAgentVersion == source.Version && components.NodeAgentManifest == source.ManifestDigest &&
		components.ClassPresent && components.ClassVersion == source.Version && components.ClassManifest == source.ManifestDigest && components.ClassUID == source.GatewayClassUID && components.ClassGeneration == source.GatewayClassGeneration &&
		components.ObservationCAUID == source.ObservationCAUID && components.ObservationTLSUID == source.ObservationTLSUID && reflect.DeepEqual(components.Images, source.Images) && reflect.DeepEqual(components.CRDIdentities, source.CRDIdentities)
}

func sourceCAForPlan(plan CertificateRotationPlan, caSecret, tlsSecret, staged *corev1.Secret) []byte {
	candidates := [][]byte{caSecret.Data["ca.crt"], tlsSecret.Data["ca.crt"]}
	for _, candidate := range candidates {
		if digestBytes(candidate) == plan.Source.ObservationCADigest {
			return append([]byte(nil), candidate...)
		}
		if bytes.HasSuffix(candidate, staged.Data["ca.crt"]) {
			old := candidate[:len(candidate)-len(staged.Data["ca.crt"])]
			if digestBytes(old) == plan.Source.ObservationCADigest {
				return append([]byte(nil), old...)
			}
		}
	}
	return nil
}

func rotationSourceCA(ctx context.Context, clients *Clients, plan CertificateRotationPlan, staged *corev1.Secret) ([]byte, error) {
	ca, err := getRotationSecret(ctx, clients, plan.Namespace, plan.Release+"-observation-ca")
	if err != nil {
		return nil, err
	}
	tlsIdentity, err := getRotationSecret(ctx, clients, plan.Namespace, plan.Release+"-observation-tls")
	if err != nil {
		return nil, err
	}
	source := sourceCAForPlan(plan, ca, tlsIdentity, staged)
	if source == nil {
		return nil, errors.New("cannot recover the reviewed source CA")
	}
	return source, nil
}

func getRotationSecret(ctx context.Context, clients *Clients, namespace, name string) (*corev1.Secret, error) {
	secret, err := clients.Kubernetes.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("read observation Secret %s/%s: %w", namespace, name, err)
	}
	return secret, nil
}

func appendPEMBundle(first, second []byte) []byte {
	bundle := append([]byte(nil), first...)
	if len(bundle) > 0 && bundle[len(bundle)-1] != '\n' {
		bundle = append(bundle, '\n')
	}
	return append(bundle, second...)
}

func updateRotationCASecret(ctx context.Context, clients *Clients, plan CertificateRotationPlan, ca []byte) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		secret, err := clients.Kubernetes.CoreV1().Secrets(plan.Namespace).Get(ctx, plan.Release+"-observation-ca", metav1.GetOptions{})
		if err != nil {
			return err
		}
		if string(secret.UID) != plan.Source.ObservationCAUID {
			return errors.New("stable observation CA changed during rotation")
		}
		if err := validateReleaseOwnedInstallSecret(secret, plan.Release, corev1.SecretTypeOpaque); err != nil {
			return errors.New("stable observation CA changed during rotation")
		}
		secret.Data = map[string][]byte{"ca.crt": append([]byte(nil), ca...)}
		_, err = clients.Kubernetes.CoreV1().Secrets(plan.Namespace).Update(ctx, secret, metav1.UpdateOptions{})
		return err
	})
}

func updateRotationTLSSecret(ctx context.Context, clients *Clients, plan CertificateRotationPlan, ca []byte, staged *corev1.Secret) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		secret, err := clients.Kubernetes.CoreV1().Secrets(plan.Namespace).Get(ctx, plan.Release+"-observation-tls", metav1.GetOptions{})
		if err != nil {
			return err
		}
		if string(secret.UID) != plan.Source.ObservationTLSUID {
			return errors.New("stable observation serving Secret changed during rotation")
		}
		if err := validateReleaseOwnedInstallSecret(secret, plan.Release, corev1.SecretTypeTLS); err != nil {
			return errors.New("stable observation serving Secret changed during rotation")
		}
		secret.Data["ca.crt"] = append([]byte(nil), ca...)
		if staged != nil {
			secret.Data["tls.crt"] = append([]byte(nil), staged.Data["tls.crt"]...)
			secret.Data["tls.key"] = append([]byte(nil), staged.Data["tls.key"]...)
		}
		_, err = clients.Kubernetes.CoreV1().Secrets(plan.Namespace).Update(ctx, secret, metav1.UpdateOptions{})
		return err
	})
}

func withdrawRotationNodes(ctx context.Context, clients *Clients, plan CertificateRotationPlan) error {
	daemonSet, err := clients.Kubernetes.AppsV1().DaemonSets(plan.Namespace).Get(ctx, chartFullname(plan.Release)+"-node-agent", metav1.GetOptions{})
	if err != nil {
		return err
	}
	nodes, err := clients.Kubernetes.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	for index := range nodes.Items {
		node := &nodes.Items[index]
		if !nodeMatchesDaemonSet(node, daemonSet) {
			continue
		}
		if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			current, getErr := clients.Kubernetes.CoreV1().Nodes().Get(ctx, node.Name, metav1.GetOptions{})
			if getErr != nil {
				return getErr
			}
			delete(current.Labels, scheduling.CoreReadyLabel)
			delete(current.Labels, scheduling.CapabilityEpochLabel)
			_, updateErr := clients.Kubernetes.CoreV1().Nodes().Update(ctx, current, metav1.UpdateOptions{})
			return updateErr
		}); err != nil {
			return fmt.Errorf("withdraw Core-ready node %s: %w", node.Name, err)
		}
	}
	return nil
}

func rollRotationNodeAgent(ctx context.Context, clients *Clients, plan CertificateRotationPlan, rotationID string, capabilityHold bool) error {
	name := chartFullname(plan.Release) + "-node-agent"
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		daemonSet, err := clients.Kubernetes.AppsV1().DaemonSets(plan.Namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if daemonSet.Spec.Template.Annotations == nil {
			daemonSet.Spec.Template.Annotations = map[string]string{}
		}
		container, err := requiredContainer(daemonSet.Spec.Template.Spec.Containers, "node-agent")
		if err != nil {
			return err
		}
		currentHold, err := observationCapabilityHeld(container.Args)
		if err != nil {
			return err
		}
		if daemonSet.Spec.Template.Annotations[observationRotationKey] == rotationID && currentHold == capabilityHold {
			return nil
		}
		arguments := make([]string, 0, len(container.Args)+1)
		for _, argument := range container.Args {
			if !strings.HasPrefix(argument, "--observation-capability-hold=") {
				arguments = append(arguments, argument)
			}
		}
		if capabilityHold {
			arguments = append(arguments, "--observation-capability-hold=true")
		}
		for index := range daemonSet.Spec.Template.Spec.Containers {
			if daemonSet.Spec.Template.Spec.Containers[index].Name == "node-agent" {
				daemonSet.Spec.Template.Spec.Containers[index].Args = arguments
			}
		}
		daemonSet.Spec.Template.Annotations[observationRotationKey] = rotationID
		_, err = clients.Kubernetes.AppsV1().DaemonSets(plan.Namespace).Update(ctx, daemonSet, metav1.UpdateOptions{})
		return err
	}); err != nil {
		return fmt.Errorf("roll node agent for observation trust: %w", err)
	}
	return wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 5*time.Minute, true, func(ctx context.Context) (bool, error) {
		daemonSet, err := clients.Kubernetes.AppsV1().DaemonSets(plan.Namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		return daemonSet.Status.ObservedGeneration >= daemonSet.Generation && daemonSet.Status.DesiredNumberScheduled > 0 && daemonSet.Status.UpdatedNumberScheduled == daemonSet.Status.DesiredNumberScheduled && daemonSet.Status.NumberReady == daemonSet.Status.DesiredNumberScheduled && daemonSet.Status.NumberUnavailable == 0, nil
	})
}

func waitRotationNodesObserved(ctx context.Context, clients *Clients, plan CertificateRotationPlan, after time.Time) error {
	daemonSet, err := clients.Kubernetes.AppsV1().DaemonSets(plan.Namespace).Get(ctx, chartFullname(plan.Release)+"-node-agent", metav1.GetOptions{})
	if err != nil {
		return err
	}
	return wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 2*time.Minute, true, func(ctx context.Context) (bool, error) {
		nodes, err := clients.Kubernetes.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
		if err != nil {
			return false, err
		}
		matched := 0
		for index := range nodes.Items {
			node := &nodes.Items[index]
			if !nodeMatchesDaemonSet(node, daemonSet) {
				continue
			}
			matched++
			epoch, parseErr := strconv.ParseInt(node.Labels[scheduling.ObservationEpochLabel], 10, 64)
			if node.Labels[scheduling.CoreReadyLabel] != "" || parseErr != nil || time.Unix(0, epoch).UTC().Before(after) {
				return false, nil
			}
		}
		return matched > 0, nil
	})
}

func waitRotationNodesReady(ctx context.Context, clients *Clients, plan CertificateRotationPlan, after time.Time) error {
	daemonSet, err := clients.Kubernetes.AppsV1().DaemonSets(plan.Namespace).Get(ctx, chartFullname(plan.Release)+"-node-agent", metav1.GetOptions{})
	if err != nil {
		return err
	}
	return wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 2*time.Minute, true, func(ctx context.Context) (bool, error) {
		nodes, err := clients.Kubernetes.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
		if err != nil {
			return false, err
		}
		matched := 0
		for index := range nodes.Items {
			node := &nodes.Items[index]
			if !nodeMatchesDaemonSet(node, daemonSet) {
				continue
			}
			matched++
			epoch, parseErr := strconv.ParseInt(node.Labels[scheduling.CapabilityEpochLabel], 10, 64)
			if node.Labels[scheduling.CoreReadyLabel] != "true" || parseErr != nil || time.Unix(epoch, 0).UTC().Before(after.Add(-time.Second)) {
				return false, nil
			}
		}
		return matched > 0, nil
	})
}

func nodeMatchesDaemonSet(node *corev1.Node, daemonSet *appsv1.DaemonSet) bool {
	for key, value := range daemonSet.Spec.Template.Spec.NodeSelector {
		if node.Labels[key] != value {
			return false
		}
	}
	return true
}

func completeCertificateRotation(ctx context.Context, clients *Clients, journal certificateRotationJournal) error {
	plan := journal.Plan
	object, err := clients.Kubernetes.CoreV1().ConfigMaps(plan.Namespace).Get(ctx, certificateRotationJournalName(plan.Release), metav1.GetOptions{})
	if err != nil {
		return err
	}
	if object.Annotations[certificateRotationPlanKey] != plan.PlanID {
		return errors.New("refusing to remove a different certificate rotation journal")
	}
	staged, err := clients.Kubernetes.CoreV1().Secrets(plan.Namespace).Get(ctx, certificateRotationStagedSecretName(plan.Release), metav1.GetOptions{})
	if err != nil {
		return err
	}
	if err := validateStagedCertificate(journal, staged); err != nil {
		return err
	}
	stagedUID := staged.UID
	if err := clients.Kubernetes.CoreV1().Secrets(plan.Namespace).Delete(ctx, staged.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &stagedUID}}); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return deleteCertificateRotationJournal(ctx, clients, journal)
}

func completedRotationWithoutStaged(ctx context.Context, clients *Clients, journal certificateRotationJournal) (bool, error) {
	components, err := observeDeployedReleaseComponents(ctx, clients, journal.Plan.Namespace, journal.Plan.Release)
	if err != nil {
		return false, err
	}
	if !sameRotationRelease(components, journal.Plan.Source) || components.ObservationRotationID != journal.Plan.PlanID || components.ObservationCapabilityHeld || components.ObservationCADigest != journal.TargetCADigest || components.ObservationServingDigest != journal.TargetServingDigest {
		return false, nil
	}
	ca, err := getRotationSecret(ctx, clients, journal.Plan.Namespace, journal.Plan.Release+"-observation-ca")
	if err != nil {
		return false, err
	}
	tlsIdentity, err := getRotationSecret(ctx, clients, journal.Plan.Namespace, journal.Plan.Release+"-observation-tls")
	if err != nil {
		return false, err
	}
	if !bytes.Equal(ca.Data["ca.crt"], tlsIdentity.Data["ca.crt"]) || digestBytes(ca.Data["ca.crt"]) != journal.TargetCADigest {
		return false, nil
	}
	return true, nil
}

func deleteCertificateRotationJournal(ctx context.Context, clients *Clients, journal certificateRotationJournal) error {
	object, err := clients.Kubernetes.CoreV1().ConfigMaps(journal.Plan.Namespace).Get(ctx, certificateRotationJournalName(journal.Plan.Release), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if object.Annotations[certificateRotationPlanKey] != journal.Plan.PlanID {
		return errors.New("refusing to remove a different certificate rotation journal")
	}
	uid := object.UID
	if err := clients.Kubernetes.CoreV1().ConfigMaps(journal.Plan.Namespace).Delete(ctx, object.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}}); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

func parseCABundle(data []byte) ([]*x509.Certificate, error) {
	remaining := data
	certificates := make([]*x509.Certificate, 0, 2)
	seen := map[string]struct{}{}
	now := time.Now().UTC()
	for len(bytes.TrimSpace(remaining)) > 0 {
		block, rest := pem.Decode(remaining)
		if block == nil || block.Type != "CERTIFICATE" {
			return nil, errors.New("installation CA bundle contains invalid PEM data")
		}
		certificate, err := x509.ParseCertificate(block.Bytes)
		if err != nil || !certificate.IsCA || !certificate.BasicConstraintsValid || now.Before(certificate.NotBefore) || now.After(certificate.NotAfter) {
			return nil, errors.New("installation CA bundle contains an invalid or expired CA")
		}
		key := string(certificate.Raw)
		if _, duplicate := seen[key]; duplicate {
			return nil, errors.New("installation CA bundle repeats a CA")
		}
		seen[key] = struct{}{}
		certificates = append(certificates, certificate)
		remaining = rest
	}
	if len(certificates) == 0 || len(certificates) > 2 {
		return nil, errors.New("installation CA bundle must contain one or two CAs")
	}
	return certificates, nil
}

func validateServingIdentity(secret *corev1.Secret, authorities []*x509.Certificate) error {
	if len(secret.Data) != 3 {
		return errors.New("installation TLS Secret contains unexpected keys")
	}
	pair, err := tls.X509KeyPair(secret.Data["tls.crt"], secret.Data["tls.key"])
	if err != nil || len(pair.Certificate) != 1 {
		return errors.New("existing installation TLS key pair is invalid")
	}
	certificate, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return errors.New("existing installation TLS certificate is invalid")
	}
	roots := x509.NewCertPool()
	for _, authority := range authorities {
		roots.AddCert(authority)
	}
	if _, err := certificate.Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
		return errors.New("existing installation TLS certificate is invalid, expired, or signed by another CA")
	}
	return nil
}
