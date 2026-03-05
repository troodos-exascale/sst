.DEFAULT_GOAL := build

# 1. Update BINS to use mkimg
BINS := mkimg runctl sst hetzner-plugin
ARCH := $(shell go env GOARCH)
OS := $(shell go env GOOS)
ENTITLEMENTS := cmd/crosvm/crosvm.entitlements

.PHONY: build install clean generate tidy download vet test

download:
	@echo "--> Downloading Go modules..."
	go mod download

generate: download
	@echo "--> Generating protobuf code..."
	go generate ./...

tidy: generate
	@echo "--> Tidying Go modules..."
	go mod tidy

vet: tidy
	@echo "--> Vetting Go code..."
	go vet ./...

test: tidy
	@echo "--> Running tests..."
	go test -v ./...

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
	@# 2. Install Templates System-Wide
	@if [ -d "templates" ]; then \
		echo "--> Installing templates to /usr/local/share/mkimg/templates..."; \
		sudo mkdir -p /usr/local/share/mkimg/templates; \
		sudo cp -r templates/* /usr/local/share/mkimg/templates/ || true; \
	fi

clean:
	rm -rf build
	find . -name '*.pb.go' -delete
	find . -name '*_grpc.pb.go' -delete
