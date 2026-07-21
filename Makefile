# SPDX-FileCopyrightText: 2026 Playground Logic LLC
# SPDX-License-Identifier: Apache-2.0

.PHONY: help build check fmt vet test vuln clean

help:
	@echo "build  - build bin/vendor"
	@echo "check  - gofmt check + go vet + go test"
	@echo "fmt    - gofmt -w"
	@echo "vet    - go vet"
	@echo "test   - go test ./..."
	@echo "vuln   - govulncheck ./..."
	@echo "clean  - remove bin/"

build:
	go build -o bin/vendor ./cmd/vendor

fmt:
	gofmt -w .

vet:
	go vet ./...

test:
	go test ./...

check:
	@test -z "$$(gofmt -l . | tee /dev/stderr)" || (echo "gofmt needed"; exit 1)
	go vet ./...
	go test ./...

vuln:
	go install golang.org/x/vuln/cmd/govulncheck@latest
	govulncheck ./...

clean:
	rm -rf bin/
