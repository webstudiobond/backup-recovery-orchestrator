VERSION ?= dev

.PHONY: check test coverage vulncheck verify security-trivy \
        secrets-scan build-amd64 build-arm64 clean

check:
	@test -z "$$(gofmt -s -l ./cmd ./internal)" || (echo "Unformatted files found. Run 'gofmt -s -w ./cmd ./internal' to fix them." && false)
	golangci-lint run ./...
	go build ./...

test:
	go test -v -race ./...

coverage:
	go test -v -race -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out

vulncheck:
	govulncheck ./...

security-trivy:
	trivy fs --severity CRITICAL,HIGH .

secrets-scan:
	gitleaks detect --source . --no-git --no-color -v --redact

verify: check test vulncheck security-trivy secrets-scan

build-amd64:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w -X main.AppVersion=$(VERSION)" -o bro ./cmd/bro

build-arm64:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "-s -w -X main.AppVersion=$(VERSION)" -o bro ./cmd/bro

clean:
	rm -rf coverage coverage.out coverage.html *.out bro
