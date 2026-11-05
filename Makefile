VERSION        ?= dev
LDFLAGS        := -s -w -X main.Version=$(VERSION)
STATICCHECK    := go run honnef.co/go/tools/cmd/staticcheck@2026.2.2
CONTROLLER_GEN := go run sigs.k8s.io/controller-tools/cmd/controller-gen@v0.23.0
X86            := x86_64-unknown-linux-musl
ARM64          := aarch64-unknown-linux-musl

.PHONY: all check central-check edge-check fmt build build-edge-arm64 \
	generate manifests images images-arm64 e2e load contract edge-budget clean \
	release-binaries release-manifests

all: check build

check: central-check edge-check

central-check:
	@out="$$(cd central && gofmt -l .)"; \
	if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi
	cd central && go vet ./...
	cd central && $(STATICCHECK) ./...
	cd central && go test ./...

edge-check:
	cd edge && cargo fmt --check
	cd edge && cargo clippy --all-targets -- -D warnings
	cd edge && cargo test

contract:
	cd central && go test ./internal/wire -run Contract
	cd edge && cargo test --test contract

fmt:
	cd central && gofmt -w .
	cd edge && cargo fmt

build:
	mkdir -p bin
	cd central && CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' \
		-o ../bin/babis-operator ./cmd/babis-operator
	cd edge && cargo zigbuild --release --target $(X86)
	cp edge/target/$(X86)/release/babis bin/babis
	cp bin/babis bin/babis-linux-amd64
	cp bin/babis-operator bin/babis-operator-linux-amd64

build-edge-arm64:
	cd edge && cargo zigbuild --release --target $(ARM64)
	mkdir -p bin
	cp edge/target/$(ARM64)/release/babis bin/babis-linux-arm64

# Release musl binary must be <= 3 MB and peak RSS <= 5 MB at 300 Applications.
edge-budget:
	cd edge && cargo zigbuild --release --target $(X86)
	@size=$$(stat -c %s edge/target/$(X86)/release/babis); \
	echo "binary: $$size bytes"; \
	if [ "$$size" -gt 3000000 ]; then echo "FAIL: binary > 3 MB"; exit 1; fi
	mkdir -p edge/target/budget
	python3 -I test/budget/run.py edge/target/$(X86)/release/babis edge/target/budget 5 5120

generate:
	cd central && $(CONTROLLER_GEN) object paths=./api/...

manifests:
	cd central && $(CONTROLLER_GEN) crd rbac:roleName=babis-operator paths=./... \
		output:crd:dir=config/crd output:rbac:dir=config/rbac

# Static release binaries for both architectures, plus a checksum file.
release-binaries:
	mkdir -p bin
	cd edge && cargo zigbuild --release --target $(X86)
	cd edge && cargo zigbuild --release --target $(ARM64)
	cp edge/target/$(X86)/release/babis bin/babis-linux-amd64
	cp edge/target/$(ARM64)/release/babis bin/babis-linux-arm64
	cd central && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags '$(LDFLAGS)' \
		-o ../bin/babis-operator-linux-amd64 ./cmd/babis-operator
	cd central && CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags '$(LDFLAGS)' \
		-o ../bin/babis-operator-linux-arm64 ./cmd/babis-operator
	cd bin && sha256sum babis-linux-amd64 babis-linux-arm64 \
		babis-operator-linux-amd64 babis-operator-linux-arm64 > SHA256SUMS

# Render the install manifests with ghcr.io image names. Use VERSION=<tag>.
# The deploy/ sources keep their :dev image names. The overlays live in
# bin/overlay/ (make clean removes them).
release-manifests:
	@set -e; reg=ghcr.io/billygrande; \
	mkdir -p bin/overlay/central bin/overlay/edge; \
	printf 'apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n  - ../../../deploy/central\nimages:\n  - name: babis-operator\n    newName: %s/babis-operator\n    newTag: %s\n' \
		"$$reg" "$(VERSION)" > bin/overlay/central/kustomization.yaml; \
	printf 'apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n  - ../../../deploy/edge\nimages:\n  - name: babis-edge\n    newName: %s/babis-edge\n    newTag: %s\n' \
		"$$reg" "$(VERSION)" > bin/overlay/edge/kustomization.yaml; \
	kubectl kustomize bin/overlay/central > bin/babis-central.yaml; \
	kubectl kustomize bin/overlay/edge > bin/babis-edge.yaml; \
	grep -q "$$reg/babis-operator:$(VERSION)" bin/babis-central.yaml; \
	grep -q "$$reg/babis-edge:$(VERSION)" bin/babis-edge.yaml; \
	if grep -q ':dev' bin/babis-central.yaml bin/babis-edge.yaml; then echo "FAIL: :dev image left"; exit 1; fi

images: build
	docker build -f images/edge.Dockerfile -t babis-edge:dev .
	docker build -f images/central.Dockerfile -t babis-operator:dev .

images-arm64: build-edge-arm64
	@docker buildx version >/dev/null 2>&1 || { echo "SKIP: docker buildx not available"; exit 0; }; \
	docker buildx build --platform linux/arm64 \
		-f images/edge.Dockerfile -t babis-edge:dev-arm64 --load . \
	|| echo "SKIP: arm64 image build failed (buildx cannot target linux/arm64 here)"

e2e:
	bash test/e2e/run.sh

load:
	bash test/load/run.sh

clean:
	rm -rf bin/ edge/target/
