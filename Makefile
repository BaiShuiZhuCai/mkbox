SHELL := /bin/bash
BINARY := mkbox
BIN_DIR := bin
DIST_DIR := dist
VERSION ?= 0.2.0
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w \
	-X main.version=$(VERSION) \
	-X main.commit=$(COMMIT) \
	-X main.date=$(DATE)
PLATFORMS := linux/amd64 linux/arm64 linux/arm

.PHONY: all build install uninstall test vet fmt check smoke strict-smoke clean cross dist source release clean-dist help

all: build

build:
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN_DIR)/$(BINARY) ./cmd/mkbox

install: build
	install -m 0755 $(BIN_DIR)/$(BINARY) $(DESTDIR)/usr/local/bin/$(BINARY)

uninstall:
	rm -f $(DESTDIR)/usr/local/bin/$(BINARY)

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -w ./cmd ./internal

check: fmt vet test

smoke:
	bash test/smoke.sh

# Rebuild release artifacts and exercise the strict artifact test suite.
strict-smoke: release
	bash test/strict-smoke.sh

clean:
	rm -rf $(BIN_DIR) $(DIST_DIR)

# Cross-compile release binaries and package them with checksums.
clean-dist:
	@mkdir -p $(DIST_DIR)
	@rm -f $(DIST_DIR)/*.tar.gz $(DIST_DIR)/checksums.txt

cross: clean-dist
	@set -e; \
	for platform in $(PLATFORMS); do \
		os=$${platform%/*}; \
		arch=$${platform#*/}; \
		name=$(BINARY)-$$os-$$arch; \
		mkdir -p $(DIST_DIR)/$$name; \
		echo "building $$os/$$arch"; \
		if [ "$$arch" = "arm" ]; then \
			GOARM=7 CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags '$(LDFLAGS)' -o $(DIST_DIR)/$$name/$(BINARY) ./cmd/mkbox; \
		else \
			CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags '$(LDFLAGS)' -o $(DIST_DIR)/$$name/$(BINARY) ./cmd/mkbox; \
		fi; \
		tar -C $(DIST_DIR)/$$name -czf $(DIST_DIR)/$$name.tar.gz $(BINARY); \
		rm -rf $(DIST_DIR)/$$name; \
	done; \
	cd $(DIST_DIR) && sha256sum *.tar.gz > checksums.txt; \
	echo "release artifacts:"; ls -lh $(CURDIR)/$(DIST_DIR)

dist: test cross

# Package the source tree without build output or VCS metadata.
source:
	@mkdir -p $(DIST_DIR)
	tar -czf $(DIST_DIR)/makebox-$(VERSION)-source.tar.gz \
		--exclude='./bin' --exclude='./dist' --exclude='./.git' \
		--transform 's,^\./,makebox-$(VERSION)/,' .
	@cd $(DIST_DIR) && sha256sum *.tar.gz | sort -k2 > checksums.txt
	@echo "source archive: $(DIST_DIR)/makebox-$(VERSION)-source.tar.gz"

release: dist source

help:
	@echo "make build    构建 bin/mkbox"
	@echo "make test     运行测试"
	@echo "make check    格式化、vet 与测试"
	@echo "make cross    交叉编译 linux/amd64、arm64、arm(7)"
	@echo "make dist     测试后生成发布包与 checksums.txt"
	@echo "make source   生成源码归档"
	@echo "make release  dist + source"
	@echo "make smoke    构建并执行 dry-run 冒烟测试"
	@echo "make strict-smoke 构建发布包并执行严格产物测试"
	@echo "make install  安装到 /usr/local/bin"
