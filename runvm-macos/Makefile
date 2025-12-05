PREFIX?=/usr/local

.PHONY: all clean install

all: build/runvm

build:
	mkdir -p build

build/runvm: build runvm/Sources/runvm/main.swift runvm/Package.swift
	cd runvm && swift build -c release --disable-sandbox
	cp runvm/.build/release/runvm build/runvm
	codesign -s - --entitlements runvm/runvm.entitlements build/runvm
	chmod +x build/runvm

clean:
	rm -rf build .build runvm/.build

install: all
	sudo install -m 755 build/runvm $(PREFIX)/bin/runvm

build/vm: build
	mkdir -p build/vm

build/vm/ubuntu: build/vm
	mkdir -p build/vm/ubuntu
	cd build/vm/ubuntu && ../../../vmbuilders/ubuntu.sh
