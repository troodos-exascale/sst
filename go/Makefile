.DEFAULT_GOAL := build

# Default list of binaries
BINS := mkimage runctl sst hetzner-plugin
ARCH := $(shell go env GOARCH)
OS := $(shell go env GOOS)
ENTITLEMENTS := cmd/crosvm/crosvm.entitlements

.PHONY: build install clean generate tidy download vet test

# 1. First: Download all known dependencies without validating imports
download:
	@echo "--> Downloading Go modules..."
	go mod download

# 2. Second: Generate the .pb.go files (now imports in code will be valid)
generate: download
	@echo "--> Generating protobuf code..."
	go generate ./...

# 3. Third: Finalize go.mod now that generated code exists on disk
tidy: generate
	@echo "--> Tidying Go modules..."
	go mod tidy

# 4. Vet: Examine Go source code and report suspicious constructs
vet: tidy
	@echo "--> Vetting Go code..."
	go vet ./...

# 5. Test: Run unit tests across the module
test: tidy
	@echo "--> Running tests..."
	go test -v ./...

# 6. Final: Build the binaries using the validated module graph
build: tidy
	@set -e; \
	for b in $(BINS); do \
		echo "--> Building $$b for ${OS}/${ARCH}..."; \
		GOOS=${OS} GOARCH=${ARCH} go build -o build/${OS}/${ARCH}/$$b ./cmd/$$b ; \
	done
	@set -e; \
	if [ "${OS}" = "darwin" ]; then \
		echo "--> Building crosvm for darwin/${ARCH}..."; \
		go build -o build/darwin/${ARCH}/crosvm ./cmd/crosvm ; \
		if [ -f $(ENTITLEMENTS) ]; then \
			echo "--> Signing crosvm with entitlements..."; \
			codesign -s - --entitlements $(ENTITLEMENTS) --force build/darwin/${ARCH}/crosvm; \
		else \
			echo "ERROR: Entitlements file $(ENTITLEMENTS) not found!"; \
			exit 1; \
		fi \
	else \
		echo "--> Skipping macOS crosvm wrapper build (using native Linux crosvm)"; \
	fi

install: build
	@for b in $(BINS); do \
		sudo install -m 755 build/${OS}/${ARCH}/$$b /usr/local/bin/$$b ; \
	done
	@if [ "${OS}" = "darwin" ]; then \
		sudo install -m 755 build/darwin/${ARCH}/crosvm /usr/local/bin/crosvm ; \
	fi

clean:
	rm -rf build
	find . -name '*.pb.go' -delete
	find . -name '*_grpc.pb.go' -delete
