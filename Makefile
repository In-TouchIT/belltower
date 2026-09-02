.PHONY: build seed run test clean docker deploy lint

BINARY_NAME=belltower
GO=go

# Default target
all: build

# Build the binary
build:
	$(GO) build -o bin/$(BINARY_NAME) cmd/$(BINARY_NAME)/main.go

# Build the seed tool
seed-tool:
	$(GO) build -o bin/seed cmd/seed/main.go

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
		--exclude 'node_modules' \
		./ ubuntu@192.168.111.122:/opt/status-page/
	ssh ubuntu@192.168.111.122 "cd /opt/status-page && sudo docker compose up -d --build"

# Run tests with coverage
test-coverage:
	$(GO) test -coverprofile=coverage.out ./...
	$(GO) tool cover -html=coverage.out

# Format code
fmt:
	$(GO) fmt ./...

# Check formatting
fmt-check:
	$(GO) fmt -l ./...
