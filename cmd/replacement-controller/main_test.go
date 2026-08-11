package main

import (
	"testing"

	"github.com/Amoenus/waycloak/internal/gatewayruntime"
)

func TestBaselineDoesNotInheritPortForwardGatewayRuntimeDefaults(t *testing.T) {
	provisioner := &gatewayruntime.Provisioner{}
	configurePortForwardGatewayRuntime(provisioner, false, "unused-image", 9443, false, 9444)

	if provisioner.PortForwardRuntimeImage != "" || provisioner.PortForwardRuntimePort != 0 || provisioner.AdapterEnabled || provisioner.AdapterPort != 0 {
		t.Fatalf("baseline provisioner inherited port-forward configuration: %#v", provisioner)
	}
}

func TestPortForwardGatewayRuntimeIsAppliedExplicitly(t *testing.T) {
	provisioner := &gatewayruntime.Provisioner{}
	configurePortForwardGatewayRuntime(provisioner, true, "runtime-image", 9443, true, 9444)

	if provisioner.PortForwardRuntimeImage != "runtime-image" || provisioner.PortForwardRuntimePort != 9443 || !provisioner.AdapterEnabled || provisioner.AdapterPort != 9444 {
		t.Fatalf("explicit port-forward configuration was not applied: %#v", provisioner)
	}
}
