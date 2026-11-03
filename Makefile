VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X github.com/kphatak/kubectl-dra/pkg/cmd.Version=$(VERSION)

# Let the go binary on PATH use its own root. A GOROOT left over from another
# Go install makes builds fail with "compile: version ... does not match".
unexport GOROOT

# golangci-lint is built from source with the local Go so that it can read
# export data from the same Go release. Prebuilt binaries made with an older
# Go fail with "export data version N is greater than maximum supported".
GOLANGCI_LINT_VERSION := v2.15.0
GOLANGCI_LINT := $(CURDIR)/bin/golangci-lint-$(GOLANGCI_LINT_VERSION)

# kind is pinned because only recent releases run the node images for the
# Kubernetes versions that serve resource.k8s.io/v1.
KIND_VERSION := v0.33.0
KIND := $(CURDIR)/bin/kind-$(KIND_VERSION)

# Release tooling, for checking a release locally before tagging. The
# release itself runs in .github/workflows/release.yaml.
GORELEASER_VERSION := v2.19.0
GORELEASER := $(CURDIR)/bin/goreleaser-$(GORELEASER_VERSION)
KREW_VERSION := v0.5.0
KREW_VALIDATE := $(CURDIR)/bin/validate-krew-manifest-$(KREW_VERSION)

.PHONY: build test lint vet e2e e2e-up e2e-test e2e-down release-check release-snapshot krew-check

build:
	go build -ldflags "$(LDFLAGS)" -o bin/kubectl-dra ./cmd/kubectl-dra

test:
	go test -race ./...

vet:
	go vet ./...

lint: $(GOLANGCI_LINT)
	$(GOLANGCI_LINT) run ./...

$(GOLANGCI_LINT):
	GOBIN=$(CURDIR)/bin go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	mv $(CURDIR)/bin/golangci-lint $@

# e2e creates a kind cluster with dra-example-driver, runs the tests in
# test/e2e and deletes the cluster. E2E_KEEP=1 keeps it; the e2e-* targets
# run one phase at a time. See test/e2e/run.sh.
e2e: $(KIND)
	KIND=$(KIND) test/e2e/run.sh all

e2e-up: $(KIND)
	KIND=$(KIND) test/e2e/run.sh up

e2e-test:
	test/e2e/run.sh test

e2e-down: $(KIND)
	KIND=$(KIND) test/e2e/run.sh down

$(KIND):
	GOBIN=$(CURDIR)/bin go install sigs.k8s.io/kind@$(KIND_VERSION)
	mv $(CURDIR)/bin/kind $@

# release-check validates .goreleaser.yaml. release-snapshot builds every
# release archive into dist/ without publishing anything. krew-check then
# renders .krew.yaml against those archives, as krew-release-bot does, runs
# krew-index's structural validation on the result (the download step is
# skipped, since the release does not exist yet), and installs every
# platform's archive with krew into a throwaway root. See
# docs/releasing.md.
release-check: $(GORELEASER)
	$(GORELEASER) check

release-snapshot: $(GORELEASER)
	$(GORELEASER) release --snapshot --clean

krew-check: $(KREW_VALIDATE)
	tag=$$(ls dist/kubectl-dra_*_checksums.txt | sed -E 's|.*kubectl-dra_(.*)_checksums.txt|\1|') && \
	hack/krew-manifest.sh $$tag > dist/dra.yaml && \
	$(KREW_VALIDATE) -manifest dist/dra.yaml -skip-install && \
	hack/krew-install-test.sh $$tag

$(GORELEASER):
	GOBIN=$(CURDIR)/bin go install github.com/goreleaser/goreleaser/v2@$(GORELEASER_VERSION)
	mv $(CURDIR)/bin/goreleaser $@

$(KREW_VALIDATE):
	GOBIN=$(CURDIR)/bin go install sigs.k8s.io/krew/cmd/validate-krew-manifest@$(KREW_VERSION)
	mv $(CURDIR)/bin/validate-krew-manifest $@
