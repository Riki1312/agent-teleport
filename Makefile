PRETTIER := npx --yes prettier@3.9.10
GOLANGCI_LINT := go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0

.PHONY: check format lint test e2e

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
