PRETTIER := npx --yes prettier@3.9.10

.PHONY: check format test e2e

check: test
	test -z "$$(gofmt -l .)"
	go vet ./...
	$(PRETTIER) --check "**/*.md"
	git diff --check

format:
	gofmt -w .
	$(PRETTIER) --write "**/*.md"

test:
	go test ./...

e2e:
	AGENT_TELEPORT_E2E_REQUIRED=1 go test -count=1 -v ./test/e2e
