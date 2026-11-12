FROM --platform=$BUILDPLATFORM golang:1-alpine AS builder
ARG TARGETOS TARGETARCH
# VERSION is the version badil reports; empty falls back to the build info.
ARG VERSION=""
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
    -ldflags "-X main.version=$VERSION" -o /badil ./cmd/badil

FROM scratch
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /badil /badil
USER 65532:65532
ENTRYPOINT ["/badil"]
