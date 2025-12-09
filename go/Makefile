BINS := mkimage runctl

.PHONY: all build install clean

all: build

build:
	go mod tidy
	for b in $(BINS); do \
		go build -o bin/$$b ./cmd/$$b ; \
	done

install:
	for b in $(BINS); do \
		sudo install -m 755 bin/$$b /usr/local/bin/$$b ; \
	done

clean:
	rm -rf bin
