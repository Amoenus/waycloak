// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package waycloakctl

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"

	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

func DefaultClientFactory(_ context.Context, kubeconfig, contextName string) (*Clients, error) {
	loading := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfig != "" {
		loading.ExplicitPath = kubeconfig
	}
	overrides := &clientcmd.ConfigOverrides{CurrentContext: contextName}
	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loading, overrides).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("load Kubernetes client configuration: %w", err)
	}
	kube, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	extensions, err := apiextensionsclient.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	discoveryClient, err := discovery.NewDiscoveryClientForConfig(config)
	if err != nil {
		return nil, err
	}
	trust := append([]byte(nil), config.CAData...)
	if len(trust) == 0 && config.CAFile != "" {
		trust, err = os.ReadFile(config.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read Kubernetes trust root: %w", err)
		}
	}
	return &Clients{Kubernetes: kube, APIExtensions: extensions, Dynamic: dynamicClient, Discovery: discoveryClient,
		ClusterServerFingerprint: fingerprintText(config.Host), ClusterTrustFingerprint: fingerprintBytes(trust)}, nil
}

func fingerprintText(value string) string { return fingerprintBytes([]byte(value)) }
func fingerprintBytes(value []byte) string {
	if len(value) == 0 {
		return ""
	}
	sum := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(sum[:])
}
