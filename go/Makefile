.DEFAULT_GOAL := build
# Default list of binaries
BINS := mkimage runctl sst hetzner-plugin
ARCH := $(shell go env GOARCH)
OS := $(shell go env GOOS)
ENTITLEMENTS := cmd/runvm/runvm.entitlements

.PHONY: build install clean generate tidy format check-entitlement test

tidy:
	go mod tidy

format:
	go fmt ./...

generate:
	go generate ./...

build: generate
	@# Loop through standard binaries for the current OS/ARCH
	@for b in $(BINS); do \
		echo "--> Building $$b for ${OS}/${ARCH}..."; \
		GOOS=${OS} GOARCH=${ARCH} go build -o build/${OS}/${ARCH}/$$b ./cmd/$$b ; \
	done
	@# ONLY build and sign runvm if on macOS (darwin)
	@if [ "${OS}" = "darwin" ]; then \
		echo "--> Building runvm for darwin/${ARCH}..."; \
		go build -o build/darwin/${ARCH}/runvm ./cmd/runvm ; \
		if [ -f $(ENTITLEMENTS) ]; then \
			echo "--> Signing runvm with entitlements..."; \
			codesign -s - --entitlements $(ENTITLEMENTS) --force build/darwin/${ARCH}/runvm; \
		else \
			echo "ERROR: Entitlements file $(ENTITLEMENTS) not found!"; \
			exit 1; \
		fi \
	else \
		echo "--> Skipping runvm build (not on macOS)"; \
	fi

install: build
	@for b in $(BINS); do \
		sudo install -m 755 build/${OS}/${ARCH}/$$b /usr/local/bin/$$b ; \
	done
	@# ONLY install runvm if on macOS (darwin)
	@if [ "${OS}" = "darwin" ]; then \
		echo "--> Installing runvm..."; \
		sudo install -m 755 build/darwin/${ARCH}/runvm /usr/local/bin/runvm ; \
	fi

# (2) Helper to build for both platforms into separate directories
build-all: generate
	@$(MAKE) OS=linux ARCH=amd64 build
	@$(MAKE) OS=linux ARCH=arm64 build
	@$(MAKE) OS=darwin ARCH=arm64 build


check-entitlement:
	@if [ "${OS}" = "darwin" ]; then \
		echo "--- Checking Build Artifact ---"; \
		codesign -d --entitlements :- build/darwin/${ARCH}/runvm || echo "Build artifact not signed"; \
		echo "\n--- Checking Installed Binary ---"; \
		codesign -d --entitlements :- /usr/local/bin/runvm || echo "Installed binary not signed"; \
	else \
		echo "Entitlement checks only applicable on macOS."; \
	fi

clean:
	rm -rf build
	find . -name '*.pb.go' -delete
	find . -name '*_grpc.pb.go' -delete

test: build
	@echo "--> Running Go tests..."
	@go test -count=1 ./...
	@echo "✓ Tests complete."
