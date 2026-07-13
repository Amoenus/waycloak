GO ?= go
CONTROLLER_GEN = $(GO) run sigs.k8s.io/controller-tools/cmd/controller-gen@v0.21.0
SETUP_ENVTEST = $(GO) run sigs.k8s.io/controller-runtime/tools/setup-envtest@v0.24.2-0.20260522131650-4e7b752653a0

.PHONY: generate manifests webhook-manifests test test-race vet envtest e2e verify-generated
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

verify-generated: generate manifests
	git diff --exit-code -- api config
