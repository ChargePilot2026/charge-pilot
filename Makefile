.PHONY: fmt lint check
fmt:
	go run ./tools/backend-check -fix -fmt-only
lint:
	go vet ./...
check:
	go run ./tools/backend-check
