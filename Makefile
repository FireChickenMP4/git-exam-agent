GO ?= go
BIN ?= bin
DIST ?= dist
VERSION ?= dev
LDFLAGS := -s -w -X exam.local/tools/internal/cli.Version=$(VERSION) -X main.version=$(VERSION)
HOST_OS := $(shell go env GOOS)
ifeq ($(HOST_OS),windows)
EXT := .exe
else
EXT :=
endif

.PHONY: all fmt fmt-check vet test lint build dist judge-image clean

all: fmt-check vet test build

fmt:
	$(GO) fmt ./...

fmt-check:
	@files="$$(gofmt -l .)"; if [ -n "$$files" ]; then echo "gofmt 未通过:"; echo "$$files"; exit 1; fi

vet:
	$(GO) vet ./...

test:
	$(GO) test ./...

lint: fmt-check vet

build:
	mkdir -p $(BIN)
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN)/examd$(EXT) ./cmd/examd
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN)/exam-agent$(EXT) ./cmd/exam-agent
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN)/exam-hook$(EXT) ./cmd/exam-hook

# 三平台构建：Windows / macOS(amd64,arm64) / Linux
dist:
	mkdir -p $(DIST)
	set -e; for target in windows/amd64 darwin/amd64 darwin/arm64 linux/amd64; do \
		os=$${target%/*}; arch=$${target#*/}; ext=""; \
		if [ "$$os" = "windows" ]; then ext=".exe"; fi; \
		echo "build $$os/$$arch"; \
		GOOS=$$os GOARCH=$$arch $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(DIST)/examd-$$os-$$arch$$ext ./cmd/examd; \
		GOOS=$$os GOARCH=$$arch $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(DIST)/exam-agent-$$os-$$arch$$ext ./cmd/exam-agent; \
		GOOS=$$os GOARCH=$$arch $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(DIST)/exam-hook-$$os-$$arch$$ext ./cmd/exam-hook; \
	done

judge-image:
	docker build -f deploy/judge.Dockerfile -t exam-go:1.26 deploy

clean:
	rm -rf $(BIN) $(DIST)
