// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package controller

import (
	"context"
	"crypto/sha256"
	"fmt"
	"path"
	"sort"
	"strings"

	wayv1 "github.com/Amoenus/waycloak/api/v1alpha1"
	waystatus "github.com/Amoenus/waycloak/internal/status"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	utilvalidation "k8s.io/apimachinery/pkg/util/validation"
)

func validEngineMountPath(mountPath string) bool {
	clean := path.Clean(mountPath)
	if mountPath != clean || !strings.HasPrefix(clean, "/") {
		return false
	}
	return strings.HasPrefix(clean, "/gluetun/") || clean == "/run/engine-native" || strings.HasPrefix(clean, "/run/engine-native/")
}

type observedEngineConfig struct {
	Digest   string
	Provider string
	Protocol string
}

func (r *GatewayReconciler) observeEngineConfig(ctx context.Context, gateway *wayv1.VPNGateway) (observedEngineConfig, string, string) {
	if gateway.Spec.Provider != nil {
		return observedEngineConfig{Provider: gateway.Spec.Provider.Name, Protocol: strings.ToLower(gateway.Spec.Provider.Protocol)}, "", ""
	}
	config := gateway.Spec.Engine.Config
	if config == nil {
		return observedEngineConfig{}, waystatus.ReasonInvalidEngineConfiguration, "engine native configuration is missing"
	}
	hash := sha256.New()
	effective := make(map[string]string)
	for _, reference := range config.EnvFrom {
		var configMap corev1.ConfigMap
		key := types.NamespacedName{Namespace: gateway.Namespace, Name: reference.Name}
		if err := r.Get(ctx, key, &configMap); err != nil {
			return observedEngineConfig{}, waystatus.ReasonEngineConfigurationUnavailable, fmt.Sprintf("engine native ConfigMap %q is unavailable", reference.Name)
		}
		if len(configMap.BinaryData) != 0 {
			return observedEngineConfig{}, waystatus.ReasonInvalidEngineConfiguration, fmt.Sprintf("engine native environment ConfigMap %q contains binary data", reference.Name)
		}
		_, _ = fmt.Fprintf(hash, "configmap:%d:%s", len(reference.Name), reference.Name)
		keys := make([]string, 0, len(configMap.Data))
		for key := range configMap.Data {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if len(utilvalidation.IsEnvVarName(key)) != 0 {
				return observedEngineConfig{}, waystatus.ReasonInvalidEngineConfiguration, fmt.Sprintf("engine native ConfigMap %q contains invalid environment key %q", reference.Name, key)
			}
			if reservedEngineSetting(key) {
				return observedEngineConfig{}, waystatus.ReasonInvalidEngineConfiguration, fmt.Sprintf("engine native ConfigMap %q sets reserved key %q", reference.Name, key)
			}
			value := configMap.Data[key]
			_, _ = fmt.Fprintf(hash, "%d:%s=%d:", len(key), key, len(value))
			_, _ = hash.Write([]byte(value))
			effective[key] = value
		}
	}
	for _, source := range config.Files {
		if source.ConfigMapRef == nil {
			continue
		}
		var configMap corev1.ConfigMap
		key := types.NamespacedName{Namespace: gateway.Namespace, Name: source.ConfigMapRef.Name}
		if err := r.Get(ctx, key, &configMap); err != nil {
			return observedEngineConfig{}, waystatus.ReasonEngineConfigurationUnavailable, fmt.Sprintf("engine native file ConfigMap %q is unavailable", source.ConfigMapRef.Name)
		}
		_, _ = fmt.Fprintf(hash, "file-configmap:%d:%s:%d:%s", len(source.ConfigMapRef.Name), source.ConfigMapRef.Name, len(source.MountPath), source.MountPath)
		keys := make([]string, 0, len(configMap.Data)+len(configMap.BinaryData))
		for key := range configMap.Data {
			keys = append(keys, key)
		}
		for key := range configMap.BinaryData {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			_, _ = fmt.Fprintf(hash, "%d:%s=", len(key), key)
			if value, ok := configMap.Data[key]; ok {
				_, _ = fmt.Fprintf(hash, "%d:", len(value))
				_, _ = hash.Write([]byte(value))
			} else {
				_, _ = fmt.Fprintf(hash, "%d:", len(configMap.BinaryData[key]))
				_, _ = hash.Write(configMap.BinaryData[key])
			}
		}
	}
	protocol := strings.ToLower(strings.TrimSpace(effective["VPN_TYPE"]))
	if protocol == "" {
		protocol = "openvpn"
	}
	providerName := strings.TrimSpace(effective["VPN_SERVICE_PROVIDER"])
	if strings.EqualFold(gateway.Spec.Engine.Type, "Gluetun") && providerName == "" {
		return observedEngineConfig{}, waystatus.ReasonInvalidEngineConfiguration, "engine native configuration requires VPN_SERVICE_PROVIDER"
	}
	return observedEngineConfig{
		Digest:   fmt.Sprintf("sha256:%x", hash.Sum(nil)),
		Provider: providerName,
		Protocol: protocol,
	}, "", ""
}

func reservedEngineSetting(key string) bool {
	if key == "FIREWALL" || strings.HasPrefix(key, "FIREWALL_") || key == "VPN_PORT_FORWARDING" || strings.HasPrefix(key, "VPN_PORT_FORWARDING_") {
		return true
	}
	switch key {
	case "DNS_ADDRESS", "HEALTH_SERVER_ADDRESS", "HTTP_CONTROL_SERVER_ADDRESS", "HTTP_CONTROL_SERVER_AUTH_CONFIG_FILEPATH", "HTTP_CONTROL_SERVER_AUTH_DEFAULT_ROLE", "PORT_FORWARD_ONLY", "PUBLICIP_ENABLED", "VPN_INTERFACE":
		return true
	default:
		return false
	}
}
