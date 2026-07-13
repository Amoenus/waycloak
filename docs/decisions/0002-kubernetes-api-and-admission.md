# ADR 0002: Kubernetes-native API with admission injection

Status: Accepted
Date: 2026-07-13

## Context

The originating homelab uses KCL traits, Crossplane, Argo CD, and ESO. A general product cannot require that stack. Network setup must happen before application traffic starts and must share the Pod network namespace.

## Decision

Plain Kubernetes CRDs and a Pod-template annotation are canonical. A mutating admission webhook injects the data-plane components. Helm is the primary installer. KCL is an optional translation layer that emits the canonical annotation/API.

## Consequences

- Any Kubernetes manifest authoring tool can consume Waycloak.
- Webhook lifecycle and availability are security-critical.
- Injection must be versioned, idempotent, observable, and protected against bypass.
- Existing Pods are not mutated; annotation changes require normal workload rollout.

## Alternatives rejected

- KCL-only trait: excludes most Kubernetes users.
- Crossplane-only composition: couples the product to a platform framework.
- Manual sidecar snippets: duplicates complex and security-sensitive configuration.
- Node-wide transparent interception: materially expands trust and CNI integration scope.
