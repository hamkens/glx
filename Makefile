.DEFAULT_GOAL := build

GO ?= go
BINARY := glx
COMMAND := ./cmd/$(BINARY)

.PHONY: build run install test lint fmt

build:
	$(GO) build -o $(BINARY) $(COMMAND)

run: build
	./$(BINARY) $(ARGS)

install:
	$(GO) install $(COMMAND)

test:
	$(GO) test $(TEST_ARGS) ./...

lint:
	$(GO) vet ./...

fmt:
	gofmt -w internal/ cmd/
