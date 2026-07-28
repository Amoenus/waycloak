// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package waycloakctl

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

const controllerFirstBootstrapValues = `cniInstaller:
  enabled: false
nodeAgent:
  enabled: false
defaultGatewayClass:
  enabled: false
`

func LoadInstallPlan(path string) (InstallPlan, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return InstallPlan{}, err
	}
	if len(data) > 2<<20 {
		return InstallPlan{}, errors.New("install plan exceeds size limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var plan InstallPlan
	if err := decoder.Decode(&plan); err != nil {
		return plan, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return plan, errors.New("install plan contains trailing JSON")
	}
	if err := plan.validate(); err != nil {
		return plan, err
	}
	return plan, nil
}

func (plan InstallPlan) validate() error {
	if plan.APIVersion != OutputAPIVersion || plan.Kind != "InstallPlan" || !validDigest(plan.PlanID) || !validDigest(plan.Manifest) || plan.InstallSequence != controllerFirstInstallSequence ||
		plan.Namespace == "" || plan.Release == "" || plan.Values == "" || plan.Chart.Repository == "" || !validDigest(plan.Chart.Digest) {
		return errors.New("install plan identity is incomplete")
	}
	wanted := digestBytes([]byte(plan.Manifest + "\x00" + plan.Namespace + "\x00" + plan.Release + "\x00" + plan.InstallSequence + "\x00" + plan.Values))
	if plan.PlanID != wanted {
		return errors.New("install plan content does not match planID")
	}
	return nil
}

func ApplyInstallPlan(ctx context.Context, clients *Clients, runner func(context.Context, string, ...string) ([]byte, error), plan InstallPlan, confirmation string) error {
	if err := plan.validate(); err != nil {
		return err
	}
	if confirmation != plan.PlanID {
		return fmt.Errorf("refusing mutation: --confirm must exactly equal %s", plan.PlanID)
	}
	if runner == nil {
		runner = defaultRunner
	}
	namespace, err := clients.Kubernetes.CoreV1().Namespaces().Get(ctx, plan.Namespace, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		namespace = &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: plan.Namespace, Labels: map[string]string{
			"pod-security.kubernetes.io/enforce": "privileged", "pod-security.kubernetes.io/audit": "restricted", "pod-security.kubernetes.io/warn": "restricted",
		}}}
		if _, err = clients.Kubernetes.CoreV1().Namespaces().Create(ctx, namespace, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("create reviewed system namespace: %w", err)
		}
	} else if err != nil {
		return err
	} else if namespace.Labels["pod-security.kubernetes.io/enforce"] != "privileged" {
		return errors.New("existing system namespace is not explicitly privileged; review and label it separately before applying the plan")
	}
	caPEM, certPEM, keyPEM, err := observationIdentity(plan.Release, plan.Namespace)
	if err != nil {
		return err
	}
	if err := createOwnedSecret(ctx, clients, plan.Namespace, plan.Release+"-observation-ca", plan.PlanID, corev1.SecretTypeOpaque, map[string][]byte{"ca.crt": caPEM}); err != nil {
		return err
	}
	if err := createOwnedSecret(ctx, clients, plan.Namespace, plan.Release+"-observation-tls", plan.PlanID, corev1.SecretTypeTLS, map[string][]byte{"ca.crt": caPEM, "tls.crt": certPEM, "tls.key": keyPEM}); err != nil {
		return err
	}
	caSecret, err := clients.Kubernetes.CoreV1().Secrets(plan.Namespace).Get(ctx, plan.Release+"-observation-ca", metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("read observation CA Secret after creation: %w", err)
	}
	tlsSecret, err := clients.Kubernetes.CoreV1().Secrets(plan.Namespace).Get(ctx, plan.Release+"-observation-tls", metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("read observation TLS Secret after creation: %w", err)
	}
	if !bytes.Equal(caSecret.Data["ca.crt"], tlsSecret.Data["ca.crt"]) {
		return errors.New("observation CA and serving identity do not share exact trust material")
	}
	directory, err := os.MkdirTemp("", "waycloak-install-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(directory)
	valuesPath := filepath.Join(directory, "values.yaml")
	if err := os.WriteFile(valuesPath, []byte(plan.Values), 0o600); err != nil {
		return err
	}
	chart := plan.Chart.Repository + "@" + plan.Chart.Digest
	deployed, err := deployedHelmRelease(ctx, clients, plan.Namespace, plan.Release)
	if err != nil {
		return err
	}
	if !deployed {
		bootstrapValuesPath := filepath.Join(directory, "controller-first-bootstrap.yaml")
		if err := os.WriteFile(bootstrapValuesPath, []byte(controllerFirstBootstrapValues), 0o600); err != nil {
			return err
		}
		output, err := runner(ctx, "helm", "upgrade", "--install", plan.Release, chart, "--namespace", plan.Namespace, "--create-namespace", "--values", valuesPath, "--values", bootstrapValuesPath, "--wait", "--timeout", "10m")
		if err != nil {
			return fmt.Errorf("controller-first Helm bootstrap failed before Core activation: %w: %s", err, bounded(output, 4096))
		}
	}
	output, err := runner(ctx, "helm", "upgrade", "--install", plan.Release, chart, "--namespace", plan.Namespace, "--create-namespace", "--values", valuesPath, "--wait", "--timeout", "10m")
	if err != nil {
		return fmt.Errorf("helm Core activation failed; keep the deny path installed while diagnosing: %w: %s", err, bounded(output, 4096))
	}
	return nil
}

func deployedHelmRelease(ctx context.Context, clients *Clients, namespace, release string) (bool, error) {
	selector := labels.Set{"owner": "helm", "name": release, "status": "deployed"}.AsSelector().String()
	secrets, err := clients.Kubernetes.CoreV1().Secrets(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return false, fmt.Errorf("detect existing Helm release: %w", err)
	}
	return len(secrets.Items) > 0, nil
}

func createOwnedSecret(ctx context.Context, clients *Clients, namespace, name, planID string, secretType corev1.SecretType, data map[string][]byte) error {
	secrets := clients.Kubernetes.CoreV1().Secrets(namespace)
	existing, err := secrets.Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		if existing.Annotations["install.waycloak.io/plan-id"] != planID {
			return fmt.Errorf("secret %s/%s exists and is not owned by this exact plan", namespace, name)
		}
		return validateInstallSecret(existing, secretType)
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	_, err = secrets.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Annotations: map[string]string{"install.waycloak.io/plan-id": planID}}, Type: secretType, Data: data}, metav1.CreateOptions{})
	return err
}

func validateInstallSecret(secret *corev1.Secret, secretType corev1.SecretType) error {
	if secret.Type != secretType || len(secret.Data["ca.crt"]) == 0 {
		return errors.New("existing installation Secret has an invalid type or CA")
	}
	caBlock, _ := pem.Decode(secret.Data["ca.crt"])
	if caBlock == nil {
		return errors.New("existing installation CA is invalid")
	}
	ca, err := x509.ParseCertificate(caBlock.Bytes)
	if err != nil || !ca.IsCA || time.Now().After(ca.NotAfter) {
		return errors.New("existing installation CA is invalid or expired")
	}
	if secretType == corev1.SecretTypeOpaque {
		if len(secret.Data) != 1 {
			return errors.New("installation CA Secret contains unexpected keys")
		}
		return nil
	}
	if len(secret.Data) != 3 {
		return errors.New("installation TLS Secret contains unexpected keys")
	}
	pair, err := tls.X509KeyPair(secret.Data["tls.crt"], secret.Data["tls.key"])
	if err != nil || len(pair.Certificate) != 1 {
		return errors.New("existing installation TLS key pair is invalid")
	}
	certificate, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil || certificate.CheckSignatureFrom(ca) != nil || time.Now().After(certificate.NotAfter) {
		return errors.New("existing installation TLS certificate is invalid, expired, or signed by another CA")
	}
	return nil
}

func observationIdentity(release, namespace string) ([]byte, []byte, []byte, error) {
	now := time.Now().UTC()
	caPublic, caPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		return nil, nil, nil, err
	}
	caTemplate := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "Waycloak installation CA"}, NotBefore: now.Add(-5 * time.Minute), NotAfter: now.AddDate(1, 0, 0), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, caPublic, caPrivate)
	if err != nil {
		return nil, nil, nil, err
	}
	serverPublic, serverPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, nil, err
	}
	serial, err = rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		return nil, nil, nil, err
	}
	dnsName := chartFullname(release) + "-controller." + namespace + ".svc"
	serverTemplate := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: dnsName}, DNSNames: []string{dnsName, chartFullname(release) + "-controller." + namespace + ".svc.cluster.local"}, NotBefore: now.Add(-5 * time.Minute), NotAfter: now.AddDate(1, 0, 0), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	serverDER, err := x509.CreateCertificate(rand.Reader, serverTemplate, caTemplate, serverPublic, caPrivate)
	if err != nil {
		return nil, nil, nil, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(serverPrivate)
	if err != nil {
		return nil, nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverDER}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), nil
}

func defaultRunner(ctx context.Context, name string, arguments ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, arguments...).CombinedOutput()
}

func bounded(value []byte, limit int) string {
	if len(value) > limit {
		value = value[:limit]
	}
	return string(value)
}
