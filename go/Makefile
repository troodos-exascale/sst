.DEFAULT_GOAL := build
BINS := mkimage runctl sst hetzner-plugin
ARCH := $(shell go env GOARCH)

.PHONY: build install clean generate tidy format

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

install: build
	for b in $(BINS); do \
		sudo install -m 755 build/${ARCH}/$$b /usr/local/bin/$$b ; \
	done

clean:
	rm -rf build
	find . -name '*.pb.go' -delete
	find . -name '*_grpc.pb.go' -delete
