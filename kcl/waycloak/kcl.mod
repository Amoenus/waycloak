[package]
name = "waycloak"
edition = "v0.12.3"
version = "0.3.4"
description = "Optional KCL schemas for the Waycloak replacement API."
include = [
    "kcl.mod",
    "v1beta1/networking_waycloak_io_v1beta1_port_forward_lease.k",
    "v1beta1/networking_waycloak_io_v1beta1_v_p_n_egress_route.k",
    "v1beta1/networking_waycloak_io_v1beta1_v_p_n_gateway.k",
    "v1beta1/networking_waycloak_io_v1beta1_v_p_n_gateway_class.k",
    "v1beta1/networking_waycloak_io_v1beta1_v_p_n_workload_binding.k",
	"v1beta1/networking_waycloak_io_v1beta1_workload_adapter.k",
    "k8s/apimachinery/pkg/apis/meta/v1/managed_fields_entry.k",
    "k8s/apimachinery/pkg/apis/meta/v1/object_meta.k",
    "k8s/apimachinery/pkg/apis/meta/v1/owner_reference.k",
    "examples/basic.k",
	"examples/private-egress.k",
	"examples/workload-adapter.k",
    "README.md",
    "LICENSE",
]

[dependencies]
