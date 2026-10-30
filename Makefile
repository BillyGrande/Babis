VERSION        ?= dev
LDFLAGS        := -s -w -X main.Version=$(VERSION)
STATICCHECK    := go run honnef.co/go/tools/cmd/staticcheck@2026.2.2
CONTROLLER_GEN := go run sigs.k8s.io/controller-tools/cmd/controller-gen@v0.23.0
X86            := x86_64-unknown-linux-musl
ARM64          := aarch64-unknown-linux-musl

.PHONY: all check central-check edge-check fmt build build-edge-arm64 \
	generate manifests images e2e contract edge-budget clean

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

images:
	@echo "not implemented yet (plan step 5)"; exit 1

e2e:
	@echo "not implemented yet (plan step 6)"; exit 1

clean:
	rm -rf bin/ edge/target/
