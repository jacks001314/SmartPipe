# syntax=docker/dockerfile:1
# SmartPipe 一体化镜像：四个 Go 服务共用一个镜像，用 command 选择进程。
# patch-verifier 会在容器内执行仓库 smartpipe.yaml 的构建命令，因此运行时保留 Go、Node 和 Git。

FROM node:22-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web ./
RUN npm run build

FROM golang:1.26-alpine AS build
WORKDIR /src
RUN apk add --no-cache git
COPY backend ./backend
COPY sdks ./sdks
ARG GOPROXY=https://goproxy.cn,direct
ENV GOPROXY=${GOPROXY} CGO_ENABLED=0 GOFLAGS=-trimpath
WORKDIR /src/backend
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_TIME=unknown
RUN LDFLAGS="-s -w -X main.version=${VERSION}" \
 && go build -ldflags="$LDFLAGS" -o /out/admin-api ./cmd/admin-api \
 && go build -ldflags="$LDFLAGS" -o /out/bridge ./cmd/bridge \
 && go build -ldflags="$LDFLAGS" -o /out/patch-verifier ./cmd/patch-verifier \
 && go build -ldflags="$LDFLAGS" -o /out/bot ./cmd/bot \
 && go build -ldflags="$LDFLAGS" -o /out/botdiag ./cmd/botdiag \
 && printf '%s\n' "version=${VERSION}" "commit=${COMMIT}" "built_at=${BUILD_TIME}" > /out/BUILD-INFO

FROM golang:1.26-alpine
RUN apk add --no-cache ca-certificates git nodejs npm tzdata \
 && adduser -D -u 10001 smartpipe \
 && mkdir -p /data/verify /data/releases /data/go /data/go-cache /data/npm \
 && chown -R smartpipe:smartpipe /data
WORKDIR /app
COPY --from=build /out/ /app/bin/
COPY --from=web /src/web/dist /app/web
ENV PATH=/app/bin:$PATH \
    SMARTPIPE_WEB_DIST=/app/web \
    GOPROXY=https://goproxy.cn,direct \
    GOPATH=/data/go \
    GOCACHE=/data/go-cache \
    npm_config_cache=/data/npm \
    TZ=Asia/Shanghai
USER smartpipe
EXPOSE 8090 8092 8093 8095
CMD ["admin-api"]
