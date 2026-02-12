.PHONY: test vet lint check

test:
	go test ./...

vet:
	go vet ./...

lint:
	golangci-lint run

check: test vet lint
