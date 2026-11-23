VERSION       ?= dev
SHELL_SCRIPTS := $(shell find scripts -type f -name '*.sh')
TEST_SCRIPTS  := $(shell find tests -type f -name '*.sh')
ALL_SCRIPTS   := $(SHELL_SCRIPTS) $(TEST_SCRIPTS)
COVERAGE_DIR  := coverage

.PHONY: go-check go-test go-coverage go-vulncheck go-verify security-trivy \
        shell-lint shell-fmt shell-test shell-coverage shell-verify secrets-scan \
        verify coverage build build-amd64 build-arm64 clean

go-check:
	@test -z "$$(gofmt -s -l .)" || (echo "Unformatted files found. Run 'gofmt -s -w .' to fix them." && false)
	golangci-lint run ./...
	go build ./...

go-test:
	go test -v -race ./...

go-coverage:
	go test -v -race -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out

go-vulncheck:
	govulncheck ./...

go-verify: go-check go-test go-vulncheck

shell-lint:
	shellcheck --enable=all -x $(ALL_SCRIPTS)
	shfmt -d -s -i 2 $(ALL_SCRIPTS)
	checkbashisms -f $(ALL_SCRIPTS) || true

shell-fmt:
	shfmt -w -s -i 2 $(ALL_SCRIPTS)

shell-test:
	bashunit tests/

shell-coverage:
	rm -rf $(COVERAGE_DIR)
	bashunit tests/ --coverage --coverage-paths scripts

shell-verify: shell-lint shell-test

security-trivy:
	trivy fs --severity CRITICAL,HIGH .

secrets-scan:
	gitleaks detect --source . --no-git --no-color -v --redact

verify: go-verify shell-verify security-trivy secrets-scan

coverage: go-coverage shell-coverage

build:
	go build -ldflags "-s -w -X main.AppVersion=$(VERSION)" -o bro ./cmd/bro

build-amd64:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w -X main.AppVersion=$(VERSION)" -o bro ./cmd/bro

build-arm64:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "-s -w -X main.AppVersion=$(VERSION)" -o bro ./cmd/bro

clean:
	rm -rf $(COVERAGE_DIR) .bashunit coverage.out coverage.html *.out bro release_assets dist
