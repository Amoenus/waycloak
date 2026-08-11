GO ?= go
CONTROLLER_GEN = $(GO) run sigs.k8s.io/controller-tools/cmd/controller-gen@v0.21.0
SETUP_ENVTEST = $(GO) run sigs.k8s.io/controller-runtime/tools/setup-envtest@v0.24.2-0.20260522131650-4e7b752653a0
KO = $(GO) run github.com/google/ko@v0.19.1
ACTIONLINT = $(GO) run github.com/rhysd/actionlint/cmd/actionlint@v1.7.7
NODE_AGENT_IMAGE_REPOSITORY ?= waycloak.invalid/waycloak-node-agent
WAYCLOAK_CNI_IMAGE_REPOSITORY ?= waycloak.invalid/waycloak-cni
REPLACEMENT_CONTROLLER_IMAGE_REPOSITORY ?= waycloak.invalid/waycloak-replacement-controller
GATEWAY_RUNTIME_IMAGE_REPOSITORY ?= waycloak.invalid/waycloak-gateway-runtime
GATEWAY_AGENT_IMAGE_REPOSITORY ?= waycloak.invalid/waycloak-gateway-agent
QBITTORRENT_ADAPTER_IMAGE_REPOSITORY ?= waycloak.invalid/waycloak-qbittorrent-adapter
NODE_AGENT_OCI_LAYOUT ?= dist/node-agent
WAYCLOAK_CNI_OCI_LAYOUT ?= dist/waycloak-cni
REPLACEMENT_CONTROLLER_OCI_LAYOUT ?= dist/replacement-controller
GATEWAY_RUNTIME_OCI_LAYOUT ?= dist/gateway-runtime
GATEWAY_AGENT_OCI_LAYOUT ?= dist/gateway-agent
QBITTORRENT_ADAPTER_OCI_LAYOUT ?= dist/qbittorrent-adapter
WAYCLOAKCTL_DIST ?= dist/waycloakctl
WAYCLOAK_VERSION ?= development
CHART_PACKAGE_DIR ?= dist/chart
KCL_MODULE_DIR ?= kcl/waycloak
KCL_PACKAGE_DIR ?= dist/kcl

.PHONY: generate manifests api-reference test test-race vet envtest e2e baseline-runtime-images-oci release-runtime-images-oci waycloak-cni-image-oci node-agent-image-oci replacement-controller-image-oci gateway-runtime-image-oci gateway-agent-image-oci qbittorrent-adapter-image-oci waycloakctl-release chart-package kcl-package alpha-audit api-freeze-audit verify-generated verify-chart-generated verify-kcl-generated verify-workflows
generate:
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./api/v1beta1"

manifests:
	$(CONTROLLER_GEN) crd paths="./api/v1beta1" output:crd:artifacts:config=config/crd/bases
	$(CONTROLLER_GEN) rbac:roleName=waycloak-controller,fileName=controller-role.yaml paths="./internal/rbac/controller" output:rbac:artifacts:config=config/rbac
	$(CONTROLLER_GEN) rbac:roleName=waycloak-distribution,fileName=distribution-role.yaml paths="./internal/rbac/distribution" output:rbac:artifacts:config=config/rbac
	$(CONTROLLER_GEN) rbac:roleName=waycloak-network-operator,fileName=network-operator-role.yaml paths="./internal/rbac/networkoperator" output:rbac:artifacts:config=config/rbac
	$(CONTROLLER_GEN) rbac:roleName=waycloak-workload-owner,fileName=workload-owner-role.yaml paths="./internal/rbac/workloadowner" output:rbac:artifacts:config=config/rbac
	$(CONTROLLER_GEN) rbac:roleName=waycloak-adapter-operator,fileName=adapter-operator-role.yaml paths="./internal/rbac/adapteroperator" output:rbac:artifacts:config=config/rbac
	$(CONTROLLER_GEN) rbac:roleName=waycloak-node-agent,fileName=node-agent-role.yaml paths="./internal/rbac/nodeagent" output:rbac:artifacts:config=config/rbac
	$(CONTROLLER_GEN) rbac:roleName=waycloak-gateway-secret-reader,fileName=gateway-secret-reader-role.yaml paths="./internal/rbac/gatewaysecretreader" output:rbac:artifacts:config=config/rbac

api-reference: manifests
	$(GO) run ./hack/apireference --output docs/api/v1beta1.md

test:
	$(GO) test ./...

test-race:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

envtest:
	KUBEBUILDER_ASSETS="$$($(SETUP_ENVTEST) use -p path 1.36.x)" $(GO) test -tags=envtest ./test/replacementapi/...

e2e:
	$(GO) test -tags=e2e ./test/e2e/... -v -count=1

baseline-runtime-images-oci: replacement-controller-image-oci waycloak-cni-image-oci node-agent-image-oci gateway-agent-image-oci

release-runtime-images-oci: baseline-runtime-images-oci gateway-runtime-image-oci qbittorrent-adapter-image-oci

waycloak-cni-image-oci:
	mkdir -p $(dir $(WAYCLOAK_CNI_OCI_LAYOUT))
	KO_DOCKER_REPO=$(WAYCLOAK_CNI_IMAGE_REPOSITORY) $(KO) build --push=false --oci-layout-path=$(WAYCLOAK_CNI_OCI_LAYOUT) --sbom=spdx --platform=linux/amd64,linux/arm64 ./cmd/waycloak-cni

node-agent-image-oci:
	mkdir -p $(dir $(NODE_AGENT_OCI_LAYOUT))
	KO_DOCKER_REPO=$(NODE_AGENT_IMAGE_REPOSITORY) $(KO) build --push=false --oci-layout-path=$(NODE_AGENT_OCI_LAYOUT) --sbom=spdx --platform=linux/amd64,linux/arm64 ./cmd/waycloak-node-agent

replacement-controller-image-oci:
	mkdir -p $(dir $(REPLACEMENT_CONTROLLER_OCI_LAYOUT))
	KO_DOCKER_REPO=$(REPLACEMENT_CONTROLLER_IMAGE_REPOSITORY) $(KO) build --push=false --oci-layout-path=$(REPLACEMENT_CONTROLLER_OCI_LAYOUT) --sbom=spdx --platform=linux/amd64,linux/arm64 ./cmd/replacement-controller

gateway-runtime-image-oci:
	mkdir -p $(dir $(GATEWAY_RUNTIME_OCI_LAYOUT))
	KO_DOCKER_REPO=$(GATEWAY_RUNTIME_IMAGE_REPOSITORY) $(KO) build --push=false --oci-layout-path=$(GATEWAY_RUNTIME_OCI_LAYOUT) --sbom=spdx --platform=linux/amd64,linux/arm64 ./cmd/waycloak-gateway-runtime

gateway-agent-image-oci:
	mkdir -p $(dir $(GATEWAY_AGENT_OCI_LAYOUT))
	KO_DOCKER_REPO=$(GATEWAY_AGENT_IMAGE_REPOSITORY) $(KO) build --push=false --oci-layout-path=$(GATEWAY_AGENT_OCI_LAYOUT) --sbom=spdx --platform=linux/amd64,linux/arm64 ./cmd/waycloak-gateway-agent

qbittorrent-adapter-image-oci:
	mkdir -p $(dir $(QBITTORRENT_ADAPTER_OCI_LAYOUT))
	KO_DOCKER_REPO=$(QBITTORRENT_ADAPTER_IMAGE_REPOSITORY) $(KO) build --push=false --oci-layout-path=$(QBITTORRENT_ADAPTER_OCI_LAYOUT) --sbom=spdx --platform=linux/amd64,linux/arm64 ./cmd/waycloak-qbittorrent-adapter

waycloakctl-release:
	mkdir -p $(WAYCLOAKCTL_DIST)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -trimpath -buildvcs=false -ldflags "-s -w -X main.version=$(WAYCLOAK_VERSION)" -o $(WAYCLOAKCTL_DIST)/waycloakctl-linux-amd64 ./cmd/waycloakctl
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build -trimpath -buildvcs=false -ldflags "-s -w -X main.version=$(WAYCLOAK_VERSION)" -o $(WAYCLOAKCTL_DIST)/waycloakctl-linux-arm64 ./cmd/waycloakctl
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 $(GO) build -trimpath -buildvcs=false -ldflags "-s -w -X main.version=$(WAYCLOAK_VERSION)" -o $(WAYCLOAKCTL_DIST)/waycloakctl-darwin-amd64 ./cmd/waycloakctl
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 $(GO) build -trimpath -buildvcs=false -ldflags "-s -w -X main.version=$(WAYCLOAK_VERSION)" -o $(WAYCLOAKCTL_DIST)/waycloakctl-darwin-arm64 ./cmd/waycloakctl
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 $(GO) build -trimpath -buildvcs=false -ldflags "-s -w -X main.version=$(WAYCLOAK_VERSION)" -o $(WAYCLOAKCTL_DIST)/waycloakctl-windows-amd64.exe ./cmd/waycloakctl
	cd $(WAYCLOAKCTL_DIST) && sha256sum waycloakctl-* > SHA256SUMS

chart-package:
	mkdir -p $(CHART_PACKAGE_DIR)
	helm package charts/waycloak --destination $(CHART_PACKAGE_DIR)

kcl-package:
	mkdir -p $(KCL_PACKAGE_DIR)
	cd $(KCL_MODULE_DIR) && kcl mod pkg --target $(abspath $(KCL_PACKAGE_DIR))

verify-chart-generated:
	diff -u config/crd/bases/networking.waycloak.io_portforwardleases.yaml charts/waycloak/crds/networking.waycloak.io_portforwardleases.yaml
	diff -u config/crd/bases/networking.waycloak.io_vpnegressroutes.yaml charts/waycloak/crds/networking.waycloak.io_vpnegressroutes.yaml
	diff -u config/crd/bases/networking.waycloak.io_vpngatewayclasses.yaml charts/waycloak/crds/networking.waycloak.io_vpngatewayclasses.yaml
	diff -u config/crd/bases/networking.waycloak.io_vpngateways.yaml charts/waycloak/crds/networking.waycloak.io_vpngateways.yaml
	diff -u config/crd/bases/networking.waycloak.io_vpnworkloadbindings.yaml charts/waycloak/crds/networking.waycloak.io_vpnworkloadbindings.yaml
	diff -u config/crd/bases/networking.waycloak.io_workloadadapters.yaml charts/waycloak/crds/networking.waycloak.io_workloadadapters.yaml
	diff -u config/rbac/controller-role.yaml charts/waycloak/files/controller-role.yaml
	diff -u config/rbac/distribution-role.yaml charts/waycloak/files/distribution-role.yaml
	diff -u config/rbac/network-operator-role.yaml charts/waycloak/files/network-operator-role.yaml
	diff -u config/rbac/workload-owner-role.yaml charts/waycloak/files/workload-owner-role.yaml
	diff -u config/rbac/adapter-operator-role.yaml charts/waycloak/files/adapter-operator-role.yaml
	diff -u config/rbac/node-agent-role.yaml charts/waycloak/files/node-agent-role.yaml
	diff -u config/rbac/gateway-secret-reader-role.yaml charts/waycloak/files/gateway-secret-reader-role.yaml

verify-kcl-generated:
	@tmp="$$(mktemp -d)"; trap 'rm -rf "$$tmp"' EXIT; \
		hack/generate-kcl-models.sh "$$tmp/models"; \
		diff -ru --exclude=.sonar "$$tmp/models/waycloak/v1beta1" "$(KCL_MODULE_DIR)/v1beta1"; \
		diff -ru --exclude=.sonar "$$tmp/models/waycloak/k8s" "$(KCL_MODULE_DIR)/k8s"

verify-workflows:
	$(ACTIONLINT)
	bash -n hack/validate-release-tag.sh
	bash -n hack/validate-release-inventory.sh
	bash -n hack/verify-release.sh

alpha-audit:
	$(GO) run ./hack/alphaaudit

api-freeze-audit:
	$(GO) run ./hack/apifreezeaudit

verify-generated: generate api-reference verify-chart-generated verify-kcl-generated
	git diff --exit-code -- api config docs/api/v1beta1.md kcl/waycloak/v1beta1 kcl/waycloak/k8s
