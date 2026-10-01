.PHONY: fmt lint test check
.NOTPARALLEL: check

fmt:
	go fmt ./...

lint:
	go vet ./...

test:
	go test ./...

check: fmt lint test
