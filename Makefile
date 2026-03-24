BINARY      := jailwrap
SYS_PREFIX  := /usr/local
SYS_BIN     := $(SYS_PREFIX)/bin

# When running under sudo (e.g. sudo make install), $(HOME) resolves to /root.
# Use SUDO_USER if set to get the real user's home directory.
REAL_USER   := $(or $(SUDO_USER),$(USER))
LOCAL_BIN   := $(shell echo ~$(REAL_USER))/.local/bin

VERSION     := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT      := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
DATE        := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS     := -s -w \
               -X main.version=$(VERSION) \
               -X main.commit=$(COMMIT) \
               -X main.date=$(DATE)

GOFLAGS     := CGO_ENABLED=0 GOFLAGS=-mod=readonly

.PHONY: all build test vet fmt check install install-local uninstall clean

all: build

## build: compile a static binary
build:
	$(GOFLAGS) go build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) .

## test: run tests with race detector
test:
	$(GOFLAGS) go test -race ./...

## vet: run go vet
vet:
	go vet ./...

## fmt: format source files
fmt:
	gofmt -w .

## check: vet + test
check: vet test

## install: install to /usr/local/bin (requires write permission)
install: build
	@if [ ! -d "$(SYS_BIN)" ]; then \
		echo "Error: $(SYS_BIN) does not exist"; exit 1; \
	fi
	@if [ ! -w "$(SYS_BIN)" ]; then \
		echo "Error: $(SYS_BIN) is not writable (try: sudo make install)"; exit 1; \
	fi
	install -m 755 $(BINARY) $(SYS_BIN)/$(BINARY)
	@echo "Installed to $(SYS_BIN)/$(BINARY)"

## install-local: install to ~/.local/bin (no sudo required)
install-local: build
	@mkdir -p $(LOCAL_BIN)
	install -m 755 $(BINARY) $(LOCAL_BIN)/$(BINARY)
	@echo "Installed to $(LOCAL_BIN)/$(BINARY)"
	@echo "Make sure $(LOCAL_BIN) is in your PATH."

## uninstall: remove installed binary (checks both locations)
uninstall:
	@removed=0; \
	if [ -f "$(SYS_BIN)/$(BINARY)" ]; then \
		rm -f "$(SYS_BIN)/$(BINARY)"; \
		echo "Removed $(SYS_BIN)/$(BINARY)"; removed=1; \
	fi; \
	if [ -f "$(LOCAL_BIN)/$(BINARY)" ]; then \
		rm -f "$(LOCAL_BIN)/$(BINARY)"; \
		echo "Removed $(LOCAL_BIN)/$(BINARY)"; removed=1; \
	fi; \
	if [ $$removed -eq 0 ]; then \
		echo "Nothing to uninstall."; \
	fi

## clean: remove build artifacts
clean:
	rm -f $(BINARY)
