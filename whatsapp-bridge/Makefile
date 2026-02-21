VERSION ?= 0.0.1

all: install build

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
	docker buildx build --platform linux/amd64,linux/arm64 -t venomdarklord/wa:v$(VERSION) --push .

deployment:
	kubectl apply -k ./manifests
