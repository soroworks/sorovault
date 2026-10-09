# Pinned to the Go version the Stellar SDK requires; see the README.
FROM golang:1.26-alpine AS build

WORKDIR /src

# Dependencies are copied first so a source-only change reuses this layer.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
RUN CGO_ENABLED=0 go build \
        -trimpath \
        -ldflags "-s -w -X main.version=${VERSION}" \
        -o /out/sorovault ./cmd/sorovault

FROM alpine:3.20

# TLS roots, needed to reach an https RPC endpoint.
RUN apk add --no-cache ca-certificates

# The templates and migrations are embedded in the binary, so the image needs
# nothing but the binary itself.
COPY --from=build /out/sorovault /usr/local/bin/sorovault

RUN adduser -D -u 10001 sorovault
USER sorovault

EXPOSE 8080

ENTRYPOINT ["sorovault"]
CMD ["serve"]
