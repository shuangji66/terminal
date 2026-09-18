# Makefile: builds the Vue frontend, embeds the static assets into the Go
# backend binary, then compiles the single self-contained terminal backend.
#
#   make              # dev (default, non-stripped)
#   make release V=1.0.1   # release build (strip + -ldflags), version override
#   make clean        # remove build artifacts
#
# The backend serves the embedded assets over its unix socket under the
# configured baseurl (TERMINAL_ADMIN_BASEURL, e.g. /app/terminal) fronted by
# nginx. Go cache/module variables default to project-local dirs.

ROOT      := $(abspath $(dir $(lastword $(MAKEFILE_LIST))))
BACKEND   := $(ROOT)/backend
FRONTEND  := $(ROOT)/frontend
EMBED     := $(BACKEND)/embed
OUT       := $(BACKEND)/terminal

# 版本号：默认 1.0.0，可经 V 覆盖（如 make release V=1.0.1）。
V         ?= 1.0.0

# 与 dsh 一致：默认把 Go 缓存放到项目本地目录；若环境中已设置
# GOCACHE/GOPATH 则沿用环境值。
GOCACHE   ?= $(ROOT)/.gocache
GOPATH    ?= $(ROOT)/.gopath
export GOCACHE GOPATH
export GOFLAGS="-buildvcs=false"
export PATH := /var/apps/nodejs_v24/target/bin:$(PATH)

.PHONY: all dev release clean

.NOTPARALLEL:

all: dev

dev: ## Dev build (default, non-stripped)
	$(MAKE) build LDFLAGS="-X main.terminalVersion=$(V)"

release: ## Release build (strip + external linking)
	$(MAKE) build LDFLAGS="-s -w -linkmode=external -X main.terminalVersion=$(V)"

build:
	@echo "==> Building frontend..."
	cd "$(FRONTEND)" && npm install --no-audit --no-fund && npm run build
	@echo "==> Copying frontend dist into embed dir..."
	rm -rf "$(EMBED)"
	mkdir -p "$(EMBED)"
	cp -r "$(FRONTEND)"/dist/* "$(EMBED)"/
	@echo "==> Building Go binary..."
	mkdir -p "$(GOCACHE)" "$(GOPATH)"
	go clean -cache
	cd "$(BACKEND)" && go build -trimpath -ldflags "$(LDFLAGS)" -o "$(OUT)" .
	@echo "==> Cleaning embed dir..."
	rm -rf "$(EMBED)"
	@echo "==> Done: $(OUT)"
	ls -lh "$(OUT)"

clean:
	rm -rf "$(EMBED)"
	rm -f "$(BACKEND)/terminal"