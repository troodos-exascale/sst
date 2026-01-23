.DEFAULT_GOAL := build
BINS := mkimage runctl sst hetzner-plugin runvm
ARCH := $(shell go env GOARCH)
ENTITLEMENTS := cmd/runvm/runvm.entitlements

.PHONY: build install clean generate tidy format check-entitlement

tidy:
	go mod tidy

format:
	go fmt ./...

generate:
	go generate ./...

build: generate
	for b in $(BINS); do \
		go build -o build/${ARCH}/$$b ./cmd/$$b ; \
	done
	# Sign runvm immediately after build (Matches Swift workflow)
	# We verify the file exists first to avoid silent failures
	@if [ -f $(ENTITLEMENTS) ]; then \
		echo "Signing build/${ARCH}/runvm with entitlements..."; \
		codesign -s - --entitlements $(ENTITLEMENTS) --force build/${ARCH}/runvm; \
	else \
		echo "ERROR: Entitlements file $(ENTITLEMENTS) not found!"; \
		exit 1; \
	fi

install: build
	for b in $(BINS); do \
		sudo install -m 755 build/${ARCH}/$$b /usr/local/bin/$$b ; \
	done

# RUN THIS if it fails again to see what the OS sees
check-entitlement:
	@echo "--- Checking Build Artifact ---"
	@codesign -d --entitlements :- build/${ARCH}/runvm || echo "Build artifact not signed"
	@echo "\n--- Checking Installed Binary ---"
	@codesign -d --entitlements :- /usr/local/bin/runvm || echo "Installed binary not signed"

clean:
	rm -rf build
	find . -name '*.pb.go' -delete
	find . -name '*_grpc.pb.go' -delete
