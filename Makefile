GO ?= go
BINARY := bin/lampa-go

.PHONY: all build run test vet fmt smoke clean

all: build

build:
	$(GO) build -o $(BINARY) ./cmd/lampa-go

run: build
	./$(BINARY) -config config.yaml

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

fmt:
	$(GO) fmt ./...

smoke: build
	./scripts/smoke.sh

clean:
	rm -rf bin
