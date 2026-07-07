GO             ?= go
VERSION        ?= dev
LDFLAGS        := -s -w -X github.com/BillyGrande/edge-deployer/internal/cli.Version=$(VERSION)
CONTROLLER_GEN := $(GO) run sigs.k8s.io/controller-tools/cmd/controller-gen@v0.21.0

.PHONY: all build test vet lint fmt check generate manifests release clean

all: check build

build:
	$(GO) build -o bin/edgedeploy ./cmd/edgedeploy
	$(GO) build -o bin/operator ./cmd/operator

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

# staticcheck: go install honnef.co/go/tools/cmd/staticcheck@latest
lint:
	staticcheck ./...

fmt:
	gofmt -l -w .

check: fmt vet lint test

# Regenerate api/ deepcopy code. Run after editing api/ types.
generate:
	$(CONTROLLER_GEN) object paths=./api/...

# Regenerate CRDs and RBAC. Run after editing api/ types or RBAC markers.
manifests:
	$(CONTROLLER_GEN) crd rbac:roleName=edge-deployer-operator paths=./... \
		output:crd:dir=config/crd output:rbac:dir=config/rbac

# Static release binaries.
release:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -trimpath -ldflags='$(LDFLAGS)' -o bin/edgedeploy-linux-amd64 ./cmd/edgedeploy
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build -trimpath -ldflags='$(LDFLAGS)' -o bin/edgedeploy-linux-arm64 ./cmd/edgedeploy
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -trimpath -ldflags='$(LDFLAGS)' -o bin/operator-linux-amd64 ./cmd/operator

clean:
	rm -rf bin/
