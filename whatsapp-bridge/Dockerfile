# Build the manager binary
FROM golang:1.25.7 AS builder

ARG TARGETARCH
ARG TARGETOS

WORKDIR /project

# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN go mod download

# Copy the go source
COPY . .

# Build
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} GO111MODULE=on go build -a -o whatsapp_backup main.go

# Use distroless as minimal base image to package the manager binary
# Refer to https://github.com/GoogleContainerTools/distroless for more details
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /project
COPY --from=builder /project/whatsapp_backup .

USER 65532:65532

ENTRYPOINT ["/project/whatsapp_backup"]