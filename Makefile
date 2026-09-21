BINARY  := pickteams
PREFIX  ?= /usr/local
DESTDIR ?=

# -trimpath keeps the paths of whatever machine built this out of the binary.
GOFLAGS := -trimpath -ldflags '-s -w'

.PHONY: build test vet check install uninstall clean

build:
	go build $(GOFLAGS) -o $(BINARY) .

test:
	go test ./...

vet:
	go vet ./...

check: vet test

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
