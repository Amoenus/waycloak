// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package waycloakctl

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type BundleManifest struct {
	APIVersion string          `json:"apiVersion"`
	Kind       string          `json:"kind"`
	Sections   []BundleSection `json:"sections"`
	Excluded   []string        `json:"excluded"`
}

type BundleSection struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Size   int    `json:"size"`
}
type EventSummary struct {
	Namespace    string `json:"namespace,omitempty"`
	InvolvedKind string `json:"involvedKind"`
	Reason       string `json:"reason"`
	Type         string `json:"type"`
	Count        int32  `json:"count"`
}

func SupportBundle(ctx context.Context, clients *Clients, overlay string) ([]byte, error) {
	preflight, preflightErr := Preflight(ctx, clients, overlay)
	doctor, doctorErr := Doctor(ctx, clients, "", "")
	events, eventErr := clients.Kubernetes.CoreV1().Events("").List(ctx, metav1.ListOptions{})
	if eventErr != nil {
		return nil, eventErr
	}
	eventSummaries := make([]EventSummary, 0, len(events.Items))
	for _, event := range events.Items {
		if !stringsEqualFold(event.InvolvedObject.APIVersion, "networking.waycloak.io/v1beta1") && event.InvolvedObject.Kind != "Pod" && event.InvolvedObject.Kind != "Node" {
			continue
		}
		eventSummaries = append(eventSummaries, EventSummary{Namespace: event.Namespace, InvolvedKind: event.InvolvedObject.Kind, Reason: event.Reason, Type: event.Type, Count: event.Count})
	}
	sort.Slice(eventSummaries, func(i, j int) bool {
		a, b := eventSummaries[i], eventSummaries[j]
		return fmt.Sprintf("%s/%s/%s/%s", a.Namespace, a.InvolvedKind, a.Reason, a.Type) < fmt.Sprintf("%s/%s/%s/%s", b.Namespace, b.InvolvedKind, b.Reason, b.Type)
	})
	sections := map[string][]byte{}
	sections["preflight.json"] = mustJSON(map[string]any{"report": preflight, "error": safeError(preflightErr)})
	sections["doctor.json"] = mustJSON(map[string]any{"report": doctor, "error": safeError(doctorErr)})
	sections["events.json"] = mustJSON(eventSummaries)
	manifest := BundleManifest{APIVersion: OutputAPIVersion, Kind: "SupportBundleManifest", Excluded: []string{"Secret objects and data", "ConfigMap data", "Pod logs", "raw event messages", "addresses and endpoints", "credentials and private keys"}}
	for name, data := range sections {
		sum := sha256.Sum256(data)
		manifest.Sections = append(manifest.Sections, BundleSection{Name: name, SHA256: "sha256:" + hex.EncodeToString(sum[:]), Size: len(data)})
	}
	sort.Slice(manifest.Sections, func(i, j int) bool { return manifest.Sections[i].Name < manifest.Sections[j].Name })
	sections["manifest.json"] = mustJSON(manifest)
	return deterministicTarGzip(sections)
}

func deterministicTarGzip(files map[string][]byte) ([]byte, error) {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	var output bytes.Buffer
	gzipWriter := gzip.NewWriter(&output)
	gzipWriter.Header.ModTime = time.Unix(0, 0)
	gzipWriter.Header.OS = 255
	tarWriter := tar.NewWriter(gzipWriter)
	for _, name := range names {
		data := files[name]
		if err := tarWriter.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(data)), ModTime: time.Unix(0, 0)}); err != nil {
			return nil, err
		}
		if _, err := tarWriter.Write(data); err != nil {
			return nil, err
		}
	}
	if err := tarWriter.Close(); err != nil {
		return nil, err
	}
	if err := gzipWriter.Close(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func mustJSON(value any) []byte {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		panic(err)
	}
	return append(data, '\n')
}
func safeError(err error) string {
	if err == nil {
		return ""
	}
	return "observation unavailable"
}
func stringsEqualFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		ca, cb := a[i], b[i]
		if ca >= 'A' && ca <= 'Z' {
			ca += 32
		}
		if cb >= 'A' && cb <= 'Z' {
			cb += 32
		}
		if ca != cb {
			return false
		}
	}
	return true
}

func runSupportBundle(ctx context.Context, arguments []string, dependencies Dependencies) error {
	flags := flag.NewFlagSet("support-bundle", flag.ContinueOnError)
	flags.SetOutput(dependencies.Stderr)
	kubeconfig, contextName, _ := clusterFlags(flags)
	output := flags.String("file", "waycloak-support-bundle.tar.gz", "output bundle path")
	overlay := flags.String("overlay-cidr", "100.96.0.0/16", "reviewed overlay CIDR")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	clients, err := dependencies.Clients(ctx, *kubeconfig, *contextName)
	if err != nil {
		return err
	}
	data, err := SupportBundle(ctx, clients, *overlay)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(*output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	return errors.Join(err, file.Close())
}
