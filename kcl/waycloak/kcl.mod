[package]
name = "waycloak"
edition = "v0.12.3"
version = "0.2.0-alpha.9"
description = "Optional KCL schemas and workload opt-in helpers for Waycloak."
include = [
    "kcl.mod",
    "helpers/annotations.k",
    "v1alpha1/networking_waycloak_io_v1alpha1_port_forward_lease.k",
    "v1alpha1/networking_waycloak_io_v1alpha1_v_p_n_gateway.k",
    "v1alpha1/networking_waycloak_io_v1alpha1_v_p_n_workload.k",
    "k8s/apimachinery/pkg/apis/meta/v1/managed_fields_entry.k",
    "k8s/apimachinery/pkg/apis/meta/v1/object_meta.k",
    "k8s/apimachinery/pkg/apis/meta/v1/owner_reference.k",
    "examples/basic.k",
    "README.md",
    "LICENSE",
]

[dependencies]
