.PHONY: build test vet lint check

build:
	@mkdir -p bin
	go build -o bin/tmux-adapter .
	go build -o bin/tmux-converter ./cmd/tmux-converter

test:
	go test ./...

vet:
	go vet ./...

lint:
	golangci-lint run

check: test vet lint
