# home-streaming-assistant — linux/amd64 only.
# Build with scripts/build-image.sh.

# Stage 1: frontend. Pinned by digest at execution time.
FROM docker.io/library/node:22.22.3-bookworm-slim@sha256:16d364eebf6b62da439dc993d9b80940c78b0ca38438452f011ab9a25c752644 AS web
WORKDIR /src
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# Stage 2: backend + packaged rclone.
FROM docker.io/library/golang:1.25.11-bookworm@sha256:49be5c3f5f2b766e5ba74e0bb690fea4fa03ebf5df8fe94665d42dfa727acf31 AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOOS=linux GOARCH=amd64
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
COPY --from=web /src/dist web/dist
COPY web/assets.go web/assets.go
RUN go build -trimpath -o /out/home-streaming-assistant ./cmd/server

# Pinned rclone with fixed SHA-256.
ADD https://downloads.rclone.org/v1.75.0/rclone-v1.75.0-linux-amd64.zip /tmp/rclone.zip
RUN echo "aa2804e08f48250e71009c727124b6341cd0288465804a9a09d14663cabafbaa  /tmp/rclone.zip" | sha256sum -c - \
 && cd /tmp \
 && python3 -c "import zipfile; zipfile.ZipFile('/tmp/rclone.zip').extract('rclone-v1.75.0-linux-amd64/rclone', '/tmp/rclone-x')" \
 && mv /tmp/rclone-x/rclone-v1.75.0-linux-amd64/rclone /out/rclone \
 && chmod 755 /out/rclone


FROM docker.io/library/debian:trixie-slim@sha256:7378260c0a098d27ac206225db5e7d92b8c7c84ea7afccc86eeb513756bbd392 AS runtime
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates \
 && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/home-streaming-assistant /usr/local/bin/home-streaming-assistant
COPY --from=build /out/rclone /usr/bin/rclone
USER 10001:10001
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/home-streaming-assistant"]
