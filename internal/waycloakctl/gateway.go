// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package waycloakctl

import (
	"errors"
	"fmt"
	"net/netip"
	"strings"
)

type GatewayRecipe struct {
	Namespace             string
	Name                  string
	ClassName             string
	ConfigMapName         string
	SecretName            string
	Provider              string
	Protocol              string
	OverlayCIDR           string
	AllowDisruptiveVerify bool
}

func RenderGatewayRecipe(recipe GatewayRecipe) (string, error) {
	if recipe.Namespace == "" || recipe.Name == "" || recipe.ClassName == "" || recipe.ConfigMapName == "" || recipe.SecretName == "" || recipe.OverlayCIDR == "" {
		return "", errors.New("namespace, gateway, class, ConfigMap, and Secret reference names are required")
	}
	if recipe.Provider != "protonvpn" || recipe.Protocol != "openvpn" {
		return "", errors.New("the baseline quick path supports only the reviewed Proton/OpenVPN recipe")
	}
	overlay, err := netip.ParsePrefix(recipe.OverlayCIDR)
	if err != nil || !overlay.Addr().Is4() || overlay.Bits() < 16 || overlay.Bits() > 29 {
		return "", errors.New("overlay CIDR must be a reviewed IPv4 /16 through /29")
	}
	for _, value := range []string{recipe.Namespace, recipe.Name, recipe.ClassName, recipe.ConfigMapName, recipe.SecretName} {
		if strings.ContainsAny(value, "\n\r\t:#{}[]") {
			return "", errors.New("resource names contain unsupported characters")
		}
	}
	verifyLabel := ""
	if recipe.AllowDisruptiveVerify {
		verifyLabel = "  labels:\n    verify.waycloak.io/dedicated: \"true\"\n"
	}
	return fmt.Sprintf(`apiVersion: v1
kind: ConfigMap
metadata:
  name: %s
  namespace: %s
data:
  VPN_SERVICE_PROVIDER: protonvpn
  VPN_TYPE: openvpn
  FIREWALL_OUTBOUND_SUBNETS: %s
  FIREWALL_INPUT_PORTS: "53,4789,18080"
---
apiVersion: networking.waycloak.io/v1beta1
kind: VPNGateway
metadata:
  name: %s
  namespace: %s
%s
spec:
  gatewayClassName: %s
  nativeConfigRefs:
    - role: networking.waycloak.io/GluetunEnvironment
      name: %s
  credentialRefs:
    - role: networking.waycloak.io/OpenVPNCredentials
      name: %s
  allowedRoutes:
    namespaces:
      from: Same
  clusterTraffic:
    mode: TunnelAll
  dns:
    mode: Gateway
	`, recipe.ConfigMapName, recipe.Namespace, overlay.Masked().String(), recipe.Name, recipe.Namespace, verifyLabel, recipe.ClassName, recipe.ConfigMapName, recipe.SecretName), nil
}
