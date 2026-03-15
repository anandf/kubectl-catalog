VERSION   ?= $(shell cat VERSION)
GIT_COMMIT = $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_DATE = $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')
LDFLAGS    = -X github.com/anandf/kubectl-catalog/cmd.version=$(VERSION) \
             -X github.com/anandf/kubectl-catalog/cmd.gitCommit=$(GIT_COMMIT) \
             -X github.com/anandf/kubectl-catalog/cmd.buildDate=$(BUILD_DATE)

.PHONY: build install clean test

build:
	go build -ldflags "$(LDFLAGS)" -o kubectl-catalog .

install:
	go install -ldflags "$(LDFLAGS)" .

test:
	go test ./...

clean:
	rm -f kubectl-catalog
