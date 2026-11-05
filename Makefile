PLUGIN_NAME ?= model-meta-sync
VERSION ?= 0.3.2
DIST_DIR ?= plugins
RELEASE_DIR ?= dist
GOOS ?= $(shell go env GOOS)
GOARCH ?= $(shell go env GOARCH)
# CC lets a cross build pick its toolchain, for example
#   make dist GOOS=linux GOARCH=amd64 CC=x86_64-linux-gnu-gcc
CC ?= $(shell go env CC)
GO_LDFLAGS ?= -s -w
VERSION_LDFLAG = -X main.pluginVersion=$(VERSION)

EXT_linux = so
EXT_freebsd = so
EXT_darwin = dylib
EXT_windows = dll
PLUGIN_EXT = $(or $(EXT_$(GOOS)),so)
PLUGIN_FILE = $(PLUGIN_NAME)-v$(VERSION).$(PLUGIN_EXT)
PLUGIN_OUTPUT ?= $(DIST_DIR)/$(GOOS)/$(GOARCH)/$(PLUGIN_FILE)

# Plugin-store release packaging (see store/registry-entry.json):
#   <id>_<version>_<goos>_<goarch>.zip  with <id>.<ext> at the zip root
#   checksums.txt                       sha256sum format over every zip
ARCHIVE = $(PLUGIN_NAME)_$(VERSION)_$(GOOS)_$(GOARCH).zip
ARCHIVE_PATH = $(RELEASE_DIR)/$(ARCHIVE)
CHECKSUMS = $(RELEASE_DIR)/checksums.txt

# The packaging tool always runs on the build host, even during a cross build
# (a GOOS/GOARCH given on the make command line is exported to recipes).
HOST_GOOS := $(shell go env GOHOSTOS)
HOST_GOARCH := $(shell go env GOHOSTARCH)
ZIPDIST = cd plugin && GOOS=$(HOST_GOOS) GOARCH=$(HOST_GOARCH) go run ./cmd/zipdist

.PHONY: build test fmt vet version-check checksum dist checksums release verify-dist clean distclean

build:
	mkdir -p $(dir $(PLUGIN_OUTPUT))
	cd plugin && CGO_ENABLED=1 GOOS=$(GOOS) GOARCH=$(GOARCH) CC="$(CC)" go build -trimpath -buildmode=c-shared \
		-ldflags "$(GO_LDFLAGS) $(VERSION_LDFLAG)" -o ../$(PLUGIN_OUTPUT) .
	rm -f $(basename $(PLUGIN_OUTPUT)).h
	$(ZIPDIST) -sha256 ../$(PLUGIN_OUTPUT)

# dist builds one platform and writes the store-shaped zip for it.
dist: build
	$(ZIPDIST) -in ../$(PLUGIN_OUTPUT) -name $(PLUGIN_NAME).$(PLUGIN_EXT) -out ../$(ARCHIVE_PATH)

# checksums covers every zip already present in $(RELEASE_DIR); run it once all
# platform zips are collected (CI release job, or a local multi-arch build).
checksums:
	$(ZIPDIST) -checksums ../$(RELEASE_DIR)

# release packages the host arch end to end and proves the archive layout.
release: dist checksums verify-dist

verify-dist:
	$(ZIPDIST) -verify ../$(ARCHIVE_PATH) -name $(PLUGIN_NAME).$(PLUGIN_EXT)

version-check:
	@set -e; declared=$$(sed -n 's/^var pluginVersion = "\(.*\)"/\1/p' plugin/main.go); \
	test -n "$$declared" || { echo "plugin/main.go has no pluginVersion default"; exit 1; }; \
	test "$$declared" = "$(VERSION)" || { echo "VERSION=$(VERSION) but plugin/main.go defaults to $$declared"; exit 1; }; \
	echo "version ok: $$declared"

checksum:
	$(ZIPDIST) -check-sha256 ../$(PLUGIN_OUTPUT).sha256

test: version-check
	cd plugin && go test ./...

fmt:
	cd plugin && gofmt -l -w .

vet:
	cd plugin && go vet ./...

clean:
	rm -rf build $(RELEASE_DIR)

distclean: clean
	rm -f $(DIST_DIR)/*/*/*.$(PLUGIN_EXT) $(DIST_DIR)/*/*/*.sha256
