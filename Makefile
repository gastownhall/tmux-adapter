.PHONY: build test vet lint check

build:
	go build -o tmux-adapter .
	go build -o tmux-converter ./cmd/tmux-converter

test:
	go test ./...

vet:
	go vet ./...

lint:
	golangci-lint run

check: test vet lint
