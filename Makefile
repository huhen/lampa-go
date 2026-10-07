GO ?= go
BINARY := bin/lampa-go

# Deploy directory for the frontend pipeline; exported so that
# scripts/update-frontend.sh (which reads FE_DEPLOY_DIR from the environment)
# and make agree on one name.
export FE_DEPLOY_DIR ?= deploy/web

.PHONY: all build run test vet fmt smoke clean \
	fe-diff fe-update fe-build fe-deploy fe-new-patch

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

fe-diff:
	./scripts/update-frontend.sh diff

fe-update:
	./scripts/update-frontend.sh update

fe-build:
	./scripts/update-frontend.sh build

fe-deploy:
	./scripts/update-frontend.sh deploy

fe-new-patch:
	@test -n "$(NAME)" || { echo "usage: make fe-new-patch NAME=010-short-name"; exit 1; }
	./scripts/update-frontend.sh new-patch $(NAME)
