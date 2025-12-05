BINARY := runctl
PREFIX ?= /usr/local
BINDIR := $(PREFIX)/bin

all: build

build:
	go build -o $(BINARY) .

install: build
	mkdir -p $(DESTDIR)$(BINDIR)
	sudo install -m 0755 $(BINARY) $(DESTDIR)$(BINDIR)/$(BINARY)

clean:
	rm -f $(BINARY)
