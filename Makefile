PLUGIN_NAME ?= model-meta-sync
VERSION ?= 0.3.0
DIST_DIR ?= plugins
GOOS ?= $(shell go env GOOS)
GOARCH ?= $(shell go env GOARCH)
GO_LDFLAGS ?= -s -w

EXT_linux = so
EXT_freebsd = so
EXT_darwin = dylib
EXT_windows = dll
PLUGIN_EXT = $(or $(EXT_$(GOOS)),so)
PLUGIN_FILE = $(PLUGIN_NAME)-v$(VERSION).$(PLUGIN_EXT)
PLUGIN_OUTPUT ?= $(DIST_DIR)/$(GOOS)/$(GOARCH)/$(PLUGIN_FILE)

.PHONY: build test fmt vet checksum clean distclean

build:
	mkdir -p $(dir $(PLUGIN_OUTPUT))
	cd plugin && CGO_ENABLED=1 GOOS=$(GOOS) GOARCH=$(GOARCH) go build -trimpath -buildmode=c-shared \
		-ldflags "$(GO_LDFLAGS)" -o ../$(PLUGIN_OUTPUT) .
	rm -f $(basename $(PLUGIN_OUTPUT)).h
	cd $(dir $(PLUGIN_OUTPUT)) && sha256sum $(notdir $(PLUGIN_OUTPUT)) > $(notdir $(PLUGIN_OUTPUT)).sha256

checksum:
	cd $(dir $(PLUGIN_OUTPUT)) && sha256sum -c $(notdir $(PLUGIN_OUTPUT)).sha256

test:
	cd plugin && go test ./...

fmt:
	cd plugin && gofmt -l -w .

vet:
	cd plugin && go vet ./...

clean:
	rm -rf build

distclean: clean
	rm -f $(DIST_DIR)/*/*/*.$(PLUGIN_EXT) $(DIST_DIR)/*/*/*.sha256
