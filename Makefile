PRETTIER := npx --yes prettier@3.9.10
GOLANGCI_LINT := go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0

# PLATFORMS are the release targets: Linux and macOS on amd64 and arm64.
PLATFORMS := darwin/amd64 darwin/arm64 linux/amd64 linux/arm64

.PHONY: check format lint test e2e dist

check: lint test
	$(PRETTIER) --check "**/*.md"
	git diff --check

format:
	gofmt -w .
	$(PRETTIER) --write "**/*.md"

lint:
	$(GOLANGCI_LINT) run

test:
	go test ./...

e2e:
	AGENT_TELEPORT_E2E_REQUIRED=1 go test -count=1 -v ./test/e2e

# dist builds one agent-teleport_<os>_<arch>.tar.gz per platform into a new
# dist directory, with the binary at the archive root, and SHA256SUMS. Build
# from a clean checkout of a tag to stamp its version.
dist:
	mkdir dist
	for platform in $(PLATFORMS); do \
		os=$${platform%/*}; arch=$${platform#*/}; build=dist/$${os}_$${arch}; \
		mkdir $$build && \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags="-s -w" -o $$build/agent-teleport ./cmd/agent-teleport && \
		cp LICENSE README.md $$build/ && \
		tar -C $$build -czf dist/agent-teleport_$${os}_$${arch}.tar.gz agent-teleport LICENSE README.md || exit 1; \
	done
	cd dist && shasum -a 256 *.tar.gz > SHA256SUMS
