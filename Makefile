# Tool versions are shared by local installation and both workflows.
GO_VERSION := 1.27.1
GOLANGCI_LINT_VERSION := 2.14.0
GORELEASER_VERSION := 2.18.2
ACTIONLINT_VERSION := 1.7.12
SHELLCHECK_VERSION := 0.11.0
.NOTPARALLEL: quality
TOOLS_DIR := $(CURDIR)/.tools/bin
export PATH := $(TOOLS_DIR):$(PATH)

.PHONY: tools test race coverage vet fmt-check lint installer-check unit-check cross-build workflow-check release-check snapshot quality

tools:
	GOBIN=$(TOOLS_DIR) go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v$(GOLANGCI_LINT_VERSION)
	GOBIN=$(TOOLS_DIR) go install github.com/goreleaser/goreleaser/v2@v$(GORELEASER_VERSION)
	GOBIN=$(TOOLS_DIR) go install github.com/rhysd/actionlint/cmd/actionlint@v$(ACTIONLINT_VERSION)

# Install ShellCheck $(SHELLCHECK_VERSION) and systemd-analyze via your OS package manager.
test:
	go test ./...
race:
	go test -race ./...
coverage:
	go test -race -coverprofile=coverage.out -covermode=atomic ./...
	go run ./scripts/checkcoverage coverage.out
vet:
	go vet ./...
fmt-check:
	@unformatted=$$(gofmt -l $$(find cmd internal scripts -name '*.go')) || exit $$?; \
	test -z "$$unformatted" || { printf '%s\n' "$$unformatted"; exit 1; }
lint:
	golangci-lint config verify
	golangci-lint run
installer-check:
	bash -n scripts/install.sh scripts/install_test.sh
	shellcheck scripts/install.sh scripts/install_test.sh
	bash scripts/install_test.sh
# Verify the exact unit settings against the staged binary, without installing on the host.
unit-check: cross-build
	@work=$$(mktemp -d); trap 'rm -rf "$$work"' EXIT; \
	sed 's|/usr/local/bin/sc200|$(CURDIR)/dist/build/sc200_linux_amd64|g' packaging/systemd/sc200.service > "$$work/sc200.service"; \
	systemd-analyze verify "$$work/sc200.service"
cross-build:
	mkdir -p dist/build
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o dist/build/sc200_linux_amd64 ./cmd/sc200
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -o dist/build/sc200_linux_arm64 ./cmd/sc200
workflow-check:
	actionlint
release-check:
	goreleaser check
snapshot: release-check
	goreleaser release --snapshot --clean
	python3 scripts/checkrelease.py dist
quality: fmt-check vet coverage lint installer-check unit-check cross-build workflow-check snapshot

.PHONY: shellcheck-version
shellcheck-version:
	@echo $(SHELLCHECK_VERSION)

.PHONY: goreleaser-version
goreleaser-version:
	@echo $(GORELEASER_VERSION)
