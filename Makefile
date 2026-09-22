BINARY  := pickteams
PREFIX  ?= /usr/local
DESTDIR ?=
DIST    ?= dist

# The version comes from the git tag when there is one, so a release binary
# can say what it is. A working tree with no tags falls back to "dev".
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

# -trimpath keeps the paths of whatever machine built this out of the binary.
GOFLAGS := -trimpath
LDFLAGS := -s -w -X main.version=$(VERSION)

# The platforms a release is built for. Everything is pure Go, including the
# SQLite driver, so these all cross-compile from anywhere with CGO off.
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64

.PHONY: build test vet check install uninstall clean dist version

build:
	go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(BINARY) .

version:
	@echo $(VERSION)

test:
	go test ./...

vet:
	go vet ./...

check: vet test

# Builds one tarball per platform into dist/, plus a checksum file. This is
# what the release workflow uploads, and it works the same by hand.
dist:
	rm -rf $(DIST)
	mkdir -p $(DIST)
	@for platform in $(PLATFORMS); do \
		os=$${platform%/*}; arch=$${platform#*/}; \
		name=$(BINARY)_$(VERSION)_$${os}_$${arch}; \
		echo "building $$name"; \
		mkdir -p $(DIST)/$$name; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch \
			go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(DIST)/$$name/$(BINARY) . || exit 1; \
		cp README.md LICENSE $(DIST)/$$name/; \
		cp -r deploy $(DIST)/$$name/; \
		tar -czf $(DIST)/$$name.tar.gz -C $(DIST) $$name || exit 1; \
		rm -rf $(DIST)/$$name; \
	done
	cd $(DIST) && sha256sum *.tar.gz > SHA256SUMS
	@echo
	@ls -1 $(DIST)

# Installs the binary and the unit, and creates an empty environment file the
# first time. It will not touch that file again, so an upgrade cannot wipe the
# password.
install: build
	install -D -m 0755 $(BINARY) $(DESTDIR)$(PREFIX)/bin/$(BINARY)
	install -D -m 0644 deploy/pickteams.service $(DESTDIR)/etc/systemd/system/pickteams.service
	@if [ -f $(DESTDIR)/etc/pickteams/env ]; then \
		echo "keeping the existing $(DESTDIR)/etc/pickteams/env"; \
	else \
		install -D -m 0600 deploy/env.example $(DESTDIR)/etc/pickteams/env; \
		echo "wrote $(DESTDIR)/etc/pickteams/env, put a password in it"; \
	fi
	@echo
	@echo "next:"
	@echo "  systemctl daemon-reload"
	@echo "  systemctl enable --now pickteams"

uninstall:
	rm -f $(DESTDIR)$(PREFIX)/bin/$(BINARY)
	rm -f $(DESTDIR)/etc/systemd/system/pickteams.service
	@echo "left /etc/pickteams/env and /var/lib/pickteams alone"

clean:
	rm -f $(BINARY)
	rm -rf $(DIST)
