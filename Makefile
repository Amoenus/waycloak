GO ?= go
CONTROLLER_GEN = $(GO) run sigs.k8s.io/controller-tools/cmd/controller-gen@v0.21.0
SETUP_ENVTEST = $(GO) run sigs.k8s.io/controller-runtime/tools/setup-envtest@v0.24.2-0.20260522131650-4e7b752653a0
KO = $(GO) run github.com/google/ko@v0.19.1
ACTIONLINT = $(GO) run github.com/rhysd/actionlint/cmd/actionlint@v1.7.7
IMAGE_REPOSITORY ?= waycloak.invalid/waycloak-agent
GATEWAY_MANAGER_IMAGE_REPOSITORY ?= waycloak.invalid/waycloak-gateway-manager
CONTROLLER_IMAGE_REPOSITORY ?= waycloak.invalid/waycloak-controller
QBITTORRENT_ADAPTER_IMAGE_REPOSITORY ?= waycloak.invalid/waycloak-qbittorrent-adapter
OCI_LAYOUT ?= dist/agent
GATEWAY_MANAGER_OCI_LAYOUT ?= dist/gateway-manager
CONTROLLER_OCI_LAYOUT ?= dist/controller
QBITTORRENT_ADAPTER_OCI_LAYOUT ?= dist/qbittorrent-adapter
CHART_PACKAGE_DIR ?= dist/chart
KCL_MODULE_DIR ?= kcl/waycloak
KCL_PACKAGE_DIR ?= dist/kcl

.PHONY: generate manifests webhook-manifests test test-race vet envtest e2e e2e-real-port-forward image-oci gateway-manager-image-oci controller-image-oci qbittorrent-adapter-image-oci chart-package kcl-package verify-generated verify-chart-generated verify-kcl-generated verify-workflows
generate:
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./api/v1alpha1"

manifests:
	$(CONTROLLER_GEN) crd paths="./api/v1alpha1" output:crd:artifacts:config=config/crd/bases
	$(CONTROLLER_GEN) rbac:roleName=waycloak-manager-role paths="./internal/controller" output:rbac:artifacts:config=config/rbac
	$(CONTROLLER_GEN) webhook paths="./cmd/controller" output:webhook:artifacts:config=config/webhook

webhook-manifests:
	kubectl kustomize config/webhook

test:
	$(GO) test ./...

test-race:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

envtest:
	KUBEBUILDER_ASSETS="$$($(SETUP_ENVTEST) use -p path 1.36.x)" $(GO) test -tags=envtest ./test/integration/...

e2e:
	$(GO) test -tags=e2e ./test/e2e/... -v -count=1

e2e-real-port-forward:
	$(GO) test -tags=e2e ./test/e2e/... -run '^TestRealProviderQBittorrentPortForward$$' -v -count=1 -timeout=2h

image-oci:
	mkdir -p $(dir $(OCI_LAYOUT))
	KO_DOCKER_REPO=$(IMAGE_REPOSITORY) $(KO) build --push=false --oci-layout-path=$(OCI_LAYOUT) --sbom=spdx --platform=linux/amd64,linux/arm64 ./cmd/agent

gateway-manager-image-oci:
	mkdir -p $(dir $(GATEWAY_MANAGER_OCI_LAYOUT))
	KO_DOCKER_REPO=$(GATEWAY_MANAGER_IMAGE_REPOSITORY) $(KO) build --push=false --oci-layout-path=$(GATEWAY_MANAGER_OCI_LAYOUT) --sbom=spdx --platform=linux/amd64,linux/arm64 ./cmd/gateway-manager

controller-image-oci:
	mkdir -p $(dir $(CONTROLLER_OCI_LAYOUT))
	KO_DOCKER_REPO=$(CONTROLLER_IMAGE_REPOSITORY) $(KO) build --push=false --oci-layout-path=$(CONTROLLER_OCI_LAYOUT) --sbom=spdx --platform=linux/amd64,linux/arm64 ./cmd/controller

qbittorrent-adapter-image-oci:
	mkdir -p $(dir $(QBITTORRENT_ADAPTER_OCI_LAYOUT))
	KO_DOCKER_REPO=$(QBITTORRENT_ADAPTER_IMAGE_REPOSITORY) $(KO) build --push=false --oci-layout-path=$(QBITTORRENT_ADAPTER_OCI_LAYOUT) --sbom=spdx --platform=linux/amd64,linux/arm64 ./cmd/qbittorrent-adapter

chart-package:
	mkdir -p $(CHART_PACKAGE_DIR)
	helm package charts/waycloak --destination $(CHART_PACKAGE_DIR)

kcl-package:
	mkdir -p $(KCL_PACKAGE_DIR)
	cd $(KCL_MODULE_DIR) && kcl mod pkg --target $(abspath $(KCL_PACKAGE_DIR))

verify-chart-generated:
	diff -u config/crd/bases/networking.waycloak.io_portforwardleases.yaml charts/waycloak/crds/networking.waycloak.io_portforwardleases.yaml
	diff -u config/crd/bases/networking.waycloak.io_vpngateways.yaml charts/waycloak/crds/networking.waycloak.io_vpngateways.yaml
	diff -u config/crd/bases/networking.waycloak.io_vpnworkloads.yaml charts/waycloak/crds/networking.waycloak.io_vpnworkloads.yaml
	diff -u config/rbac/role.yaml charts/waycloak/files/manager-role.yaml

verify-kcl-generated:
	@tmp="$$(mktemp -d)"; trap 'rm -rf "$$tmp"' EXIT; \
		hack/generate-kcl-models.sh "$$tmp/models"; \
		diff -ru "$$tmp/models/waycloak/v1alpha1" "$(KCL_MODULE_DIR)/v1alpha1"; \
		diff -ru "$$tmp/models/waycloak/k8s" "$(KCL_MODULE_DIR)/k8s"

verify-workflows:
	$(ACTIONLINT)

verify-generated: generate manifests verify-chart-generated verify-kcl-generated
	git diff --exit-code -- api config kcl/waycloak/v1alpha1 kcl/waycloak/k8s
