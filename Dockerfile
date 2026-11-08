# Web UI
FROM node:22-alpine AS web
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# Go server (DuckDB needs cgo)
FROM golang:1.26-bookworm AS go
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=1 go build -trimpath -ldflags="-s -w" -o /out/explorer ./cmd/explorer

FROM debian:bookworm-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates tzdata libstdc++6 \
 && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY --from=go /out/explorer /app/explorer
COPY --from=web /web/dist /app/web/dist
ENV DATA_DIR=/data
# Go heap target; DuckDB has its own limit (MEMORY_LIMIT). Together they stay
# below the 2 GB container limit in compose.yaml.
ENV GOMEMLIMIT=700MiB
VOLUME /data
EXPOSE 8080
CMD ["/app/explorer"]
