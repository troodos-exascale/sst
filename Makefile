.DEFAULT_GOAL := build

# 1. Update BINS to use mkimg
BINS := gitpm mkimg runctl sst hetzner-plugin
ARCH := $(shell go env GOARCH)
OS := $(shell go env GOOS)
ENTITLEMENTS := cmd/crosvm/crosvm.entitlements

# 2. Define OS-specific completion paths to bypass macOS SIP
ifeq ($(OS), darwin)
    # Dynamically find Homebrew prefix (works on both Intel and Apple Silicon)
    BREW_PREFIX   := $(shell brew --prefix 2>/dev/null || echo /usr/local)
    BASH_COMP_DIR := $(BREW_PREFIX)/etc/bash_completion.d
    ZSH_COMP_DIR  := $(BREW_PREFIX)/share/zsh/site-functions
else
    BASH_COMP_DIR := /usr/share/bash-completion/completions
    ZSH_COMP_DIR  := /usr/share/zsh/site-functions
endif

.PHONY: build install clean generate tidy download vet test completions

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

fmt:
	@echo "--> Formatting Go code..."
	go fmt ./...

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
	@$(MAKE) completions

completions:
	@echo "--> Generating shell completions..."
	@mkdir -p build/completions/bash build/completions/zsh
	@# ONLY loop over actual Cobra CLI binaries to prevent gRPC servers from hanging
	@for b in sst mkimg runctl gitpm; do \
		if ./build/${OS}/${ARCH}/$$b completion bash >/dev/null 2>&1; then \
			./build/${OS}/${ARCH}/$$b completion bash > build/completions/bash/$$b; \
			./build/${OS}/${ARCH}/$$b completion zsh > build/completions/zsh/$$b; \
		fi \
	done
    
install: build
	@for b in $(BINS); do \
		sudo install -m 755 build/${OS}/${ARCH}/$$b /usr/local/bin/$$b ; \
	done
	@if [ "${OS}" = "darwin" ]; then \
		sudo install -m 755 build/darwin/${ARCH}/crosvm /usr/local/bin/crosvm ; \
	fi
	@# Install Templates System-Wide
	@if [ -d "templates" ]; then \
		echo "--> Installing templates to /usr/local/share/mkimg/templates..."; \
		sudo mkdir -p /usr/local/share/mkimg/templates; \
		sudo cp -r templates/* /usr/local/share/mkimg/templates/ || true; \
	fi
	@echo "--> Installing shell completions..."
	@sudo mkdir -p $(BASH_COMP_DIR) $(ZSH_COMP_DIR)
	@for b in $(BINS); do \
		if [ -f build/completions/bash/$$b ]; then \
			sudo cp build/completions/bash/$$b $(BASH_COMP_DIR)/$$b; \
			sudo cp build/completions/zsh/$$b $(ZSH_COMP_DIR)/_$$b; \
		fi \
	done

clean:
	rm -rf build
	find . -name '*.pb.go' -delete
	find . -name '*_grpc.pb.go' -delete