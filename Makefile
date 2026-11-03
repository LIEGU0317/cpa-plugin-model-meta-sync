PLUGIN_NAME ?= model-meta-sync
VERSION ?= 0.2.0
BUILD_DIR ?= build
GOOS ?= $(shell go env GOOS)
GOARCH ?= $(shell go env GOARCH)
GO_LDFLAGS ?= -s -w

EXT_linux = so
EXT_freebsd = so
EXT_darwin = dylib
EXT_windows = dll
PLUGIN_EXT = $(or $(EXT_$(GOOS)),so)
PLUGIN_OUTPUT ?= $(BUILD_DIR)/$(PLUGIN_NAME)-v$(VERSION).$(PLUGIN_EXT)

.PHONY: build test fmt vet clean

build:
	mkdir -p $(BUILD_DIR)
	cd plugin && CGO_ENABLED=1 GOOS=$(GOOS) GOARCH=$(GOARCH) go build -trimpath -buildmode=c-shared \
		-ldflags "$(GO_LDFLAGS)" -o ../$(PLUGIN_OUTPUT) .
	rm -f $(basename $(PLUGIN_OUTPUT)).h

test:
	cd plugin && go test ./...

fmt:
	cd plugin && gofmt -l -w .

vet:
	cd plugin && go vet ./...

clean:
	rm -rf $(BUILD_DIR)
