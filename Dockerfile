# syntax=docker/dockerfile:1
# SmartPipe 一体化镜像：同一个镜像里带全部服务、Codex CLI 和 Go/Node 工具链。
# 运行方式：
#   app    容器：supervise admin-api bot bridge
#   worker 容器：supervise scheduler patch-verifier（后者只承担发版构建）
# patch-verifier 不再参与 Issue 修复链路，保留二进制用于手动验证与 release build。

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
 && go build -ldflags="$LDFLAGS" -o /out/scheduler ./cmd/scheduler \
 && go build -ldflags="$LDFLAGS" -o /out/supervise ./cmd/supervise \
 && go build -ldflags="$LDFLAGS" -o /out/bot ./cmd/bot \
 && go build -ldflags="$LDFLAGS" -o /out/botdiag ./cmd/botdiag \
 && go build -ldflags="$LDFLAGS" -o /out/release-service ./cmd/release-service \
 && go build -ldflags="$LDFLAGS" -o /out/release-mcp ./cmd/release-mcp \
 && go build -ldflags="$LDFLAGS" -o /out/release-executor ./cmd/release-executor \
 && printf '%s\n' "version=${VERSION}" "commit=${COMMIT}" "built_at=${BUILD_TIME}" > /out/BUILD-INFO \
 && for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64; do \
      goos=${target%/*}; goarch=${target#*/}; name=release-executor; \
      if [ "$goos" = windows ]; then name=release-executor.exe; fi; \
      mkdir -p /out/executors/${goos}-${goarch}; \
      GOOS=$goos GOARCH=$goarch CGO_ENABLED=0 go build -ldflags="$LDFLAGS" -o /out/executors/${goos}-${goarch}/$name ./cmd/release-executor; \
    done

FROM golang:1.26-alpine
ARG CODEX_VERSION=0.166.0
RUN apk add --no-cache ca-certificates git nodejs npm tzdata \
 && npm install -g "@openai/codex@${CODEX_VERSION}" --no-audit --no-fund \
 && adduser -D -u 10001 smartpipe \
 && mkdir -p /data/verify /data/workspaces /data/releases /data/go /data/go-cache /data/npm /data/repos /data/codex-home \
 && chown -R smartpipe:smartpipe /data
WORKDIR /app
COPY --from=build /out/ /app/bin/
COPY --from=web /src/web/dist /app/web
ENV PATH=/app/bin:/usr/local/bin:$PATH \
    SMARTPIPE_WEB_DIST=/app/web \
    GOPROXY=https://goproxy.cn,direct \
    GOPATH=/data/go \
    GOCACHE=/data/go-cache \
    npm_config_cache=/data/npm \
    CODEX_HOME=/data/codex-home \
    TZ=Asia/Shanghai
USER smartpipe
EXPOSE 8090 8092 8093 8095 8097 8098 8099
CMD ["supervise", "admin-api", "bot", "bridge"]

