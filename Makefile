.PHONY: build seed run test clean docker deploy lint

BINARY_NAME=belltower
GO=go

# Deploy target (override on the command line, e.g. make deploy DEPLOY_HOST=host)
DEPLOY_HOST?=192.168.111.122
DEPLOY_USER?=ubuntu
DEPLOY_PATH?=/opt/status-page

# Default target
all: build

# Build the binary
build:
	$(GO) build -o bin/$(BINARY_NAME) ./cmd/$(BINARY_NAME)

# Build the seed tool
seed-tool:
	$(GO) build -o bin/seed ./cmd/seed

# Run the seed tool to generate providers.yaml
seed: seed-tool
	./bin/seed

# Run the server
run: build
	./bin/$(BINARY_NAME)

# Run tests
test:
	$(GO) test -v ./...

# Run linter
lint:
	$(GO) vet ./...
	@test -z "$$(gofmt -l . | tee /dev/stderr)"

# Clean build artifacts
clean:
	rm -rf bin/ data/*.db data/*.db-wal data/*.db-shm

# Build docker image
docker:
	docker build -t belltower:latest .

# Deploy to Wharf
deploy: docker
	rsync -av --delete \
		--exclude 'data' \
		--exclude '.git' \
		--exclude 'bin' \
		--exclude '*.backup' \
		--exclude 'node_modules' \
		./ $(DEPLOY_USER)@$(DEPLOY_HOST):$(DEPLOY_PATH)/
	ssh $(DEPLOY_USER)@$(DEPLOY_HOST) "cd $(DEPLOY_PATH) && sudo docker compose up -d --build"

# Run tests with coverage
test-coverage:
	$(GO) test -coverprofile=coverage.out ./...
	$(GO) tool cover -html=coverage.out

# Format code
fmt:
	$(GO) fmt ./...

# Check formatting (go fmt has no -l flag; gofmt does)
fmt-check:
	@test -z "$$(gofmt -l . | tee /dev/stderr)"
