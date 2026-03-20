VERSION   ?= $(shell cat VERSION)
GIT_COMMIT = $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_DATE = $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')
LDFLAGS    = -X github.com/anandf/kubectl-catalog/cmd.version=$(VERSION) \
             -X github.com/anandf/kubectl-catalog/cmd.gitCommit=$(GIT_COMMIT) \
             -X github.com/anandf/kubectl-catalog/cmd.buildDate=$(BUILD_DATE)
BINARY     = kubectl-catalog
DIST_DIR   = dist

# All platform targets for cross-compilation
PLATFORMS = linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64

.PHONY: build install clean test lint vet fmt-check build-all checksums

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) .

install:
	go install -ldflags "$(LDFLAGS)" .

test:
	go test ./internal/... ./cmd/...

lint:
	golangci-lint run ./...

vet:
	go vet ./...

fmt-check:
	@test -z "$$(gofmt -l .)" || (echo "Files need formatting:"; gofmt -l .; exit 1)

test-e2e:
	go test ./test/e2e/... -v -timeout 30m

# Build for a single platform: make build-cross GOOS=linux GOARCH=arm64
build-cross:
	@test -n "$(GOOS)" || (echo "GOOS is required" && exit 1)
	@test -n "$(GOARCH)" || (echo "GOARCH is required" && exit 1)
	$(eval EXT := $(if $(filter windows,$(GOOS)),.exe,))
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) go build \
		-ldflags "$(LDFLAGS) -s -w" \
		-o $(DIST_DIR)/$(BINARY)-$(GOOS)-$(GOARCH)$(EXT) .

# Build for all platforms
build-all: clean-dist
	@echo "Building $(BINARY) v$(VERSION) for all platforms..."
	@for platform in $(PLATFORMS); do \
		GOOS=$${platform%/*} GOARCH=$${platform#*/} ; \
		EXT="" ; \
		if [ "$$GOOS" = "windows" ]; then EXT=".exe"; fi ; \
		echo "  Building $$GOOS/$$GOARCH..." ; \
		CGO_ENABLED=0 GOOS=$$GOOS GOARCH=$$GOARCH go build \
			-ldflags "$(LDFLAGS) -s -w" \
			-o $(DIST_DIR)/$(BINARY)-$$GOOS-$$GOARCH$$EXT . || exit 1 ; \
	done
	@echo "All binaries written to $(DIST_DIR)/"

# Generate SHA256 checksums for all binaries in dist/
checksums: build-all
	@cd $(DIST_DIR) && shasum -a 256 $(BINARY)-* > checksums.txt
	@echo "Checksums written to $(DIST_DIR)/checksums.txt"

clean:
	rm -f $(BINARY)

clean-dist:
	rm -rf $(DIST_DIR)

clean-all: clean clean-dist
