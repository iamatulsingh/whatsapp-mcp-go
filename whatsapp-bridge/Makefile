VERSION ?= 0.0.1
DOCKER_USER ?=

install:
	go get ./...

build:
	go build

run:
	go run main.go

tidy:
	go mod tidy

test:
	go test ./...

image:
	docker buildx build --platform linux/amd64,linux/arm64 -t $(DOCKER_USER)/whatsapp-mcp-go:v$(VERSION) --push .
