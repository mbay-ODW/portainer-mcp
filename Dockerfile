# syntax=docker/dockerfile:1
# -------- build stage --------
FROM golang:1.24-alpine AS build
WORKDIR /src

RUN apk add --no-cache git ca-certificates

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown

RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w -X main.Version=${VERSION} -X main.Commit=${COMMIT} -X main.BuildDate=${BUILD_DATE}" \
    -o /out/portainer-mcp \
    ./cmd/portainer-mcp

# -------- runtime stage --------
FROM alpine:3.20
RUN apk add --no-cache ca-certificates && \
    adduser -D -u 10001 mcp

WORKDIR /app
COPY --from=build /out/portainer-mcp /app/portainer-mcp
RUN chown -R mcp:mcp /app

USER mcp

ENV MCP_TRANSPORT=sse \
    PORT=8000

EXPOSE 8000

ENTRYPOINT ["/app/portainer-mcp"]
CMD ["-disable-version-check"]
