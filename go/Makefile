BINS := mkimage runctl sst hetzner-plugin
ARCH := $(shell go env GOARCH)


.PHONY: all build install clean generate tidy

all: build

tidy: generate
	go mod tidy

generate:
	go generate ./...

build: tidy
	go generate ./...
	go mod tidy
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
