# mkimage Makefile
PREFIX ?= /usr/local
BINDIR := $(PREFIX)/bin

APP := mkimage
BINARY := build/$(APP)
CMD := ./cmd/mkimage

.PHONY: all build clean deps install

all: $(BINARY)

# This ensures Go dependencies are resolved BEFORE building
deps:
	@echo "→ go mod tidy"
	go mod tidy

# Build directory
build:
	mkdir -p build

# Build mkimage (never under sudo)
$(BINARY): build deps
	@echo "→ building $(APP)"
	go build -o $(BINARY) $(CMD)
	chmod +x $(BINARY)
	@echo "✓ built $(BINARY)"

clean:
	rm -rf build bin
	@echo "✓ cleaned"

# Install INTO /usr/local/bin or chosen PREFIX/bin
# This command may be run with sudo, but it does NOT call go.
install: $(BINARY)
	@echo "→ installing $(APP) to $(BINDIR)/$(APP)"
	sudo install -m 755 $(BINARY) $(BINDIR)/$(APP)
	@echo "✓ installed"
