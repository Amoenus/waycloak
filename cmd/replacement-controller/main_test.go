package main

import (
	"testing"

	"github.com/Amoenus/waycloak/internal/gatewayruntime"
)

func TestCoreDoesNotInheritExtendedGatewayRuntimeDefaults(t *testing.T) {
	provisioner := &gatewayruntime.Provisioner{}
	configureExtendedGatewayRuntime(provisioner, false, "unused-image", 9443, false, 9444)

	if provisioner.PortForwardRuntimeImage != "" || provisioner.PortForwardRuntimePort != 0 || provisioner.AdapterEnabled || provisioner.AdapterPort != 0 {
		t.Fatalf("Core provisioner inherited Extended configuration: %#v", provisioner)
	}
}

func TestExtendedGatewayRuntimeIsAppliedExplicitly(t *testing.T) {
	provisioner := &gatewayruntime.Provisioner{}
	configureExtendedGatewayRuntime(provisioner, true, "runtime-image", 9443, true, 9444)

	if provisioner.PortForwardRuntimeImage != "runtime-image" || provisioner.PortForwardRuntimePort != 9443 || !provisioner.AdapterEnabled || provisioner.AdapterPort != 9444 {
		t.Fatalf("explicit Extended configuration was not applied: %#v", provisioner)
	}
}
