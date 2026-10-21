# syntax=docker/dockerfile:1
# 多架构（linux/amd64 + linux/arm64）：前两个阶段用 --platform=$BUILDPLATFORM 钉在构建机原生架构上，
# 目标架构只由阶段2 的 GOARCH=$TARGETARCH 交叉产出——纯 Go + CGO_ENABLED=0 天然支持。
# 若省掉 --platform，pnpm build 与 go build 会在 QEMU 模拟里跑，arm64 构建耗时会翻数倍。
# 仅最后的运行阶段是目标架构（apk add 需 QEMU，只有几秒）。
#
# ---- 阶段1：前端构建（产出 web/dist，供阶段2 go:embed 内嵌；产物与架构无关）----
FROM --platform=$BUILDPLATFORM node:22-alpine AS web
WORKDIR /src/web
RUN corepack enable
# 先只拷 manifest 做依赖安装缓存层
COPY web/package.json web/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY web/ ./
RUN pnpm build

# ---- 阶段2：后端构建（纯 Go 零 cgo，静态二进制；原生架构编译、交叉输出目标架构）----
FROM --platform=$BUILDPLATFORM golang:1.26.10-alpine AS build
WORKDIR /src
# 先只拷 go.mod/go.sum 做模块下载缓存层
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/web/dist ./web/dist
# 构建元数据经 ldflags 注入：.dockerignore 已排除 .git，镜像内无法回退到 VCS 戳记，故必须显式传参。
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_TIME=unknown
# TARGETARCH 由 buildx 按 --platform 自动注入（amd64 / arm64），单平台 docker build 时缺省为构建机架构
ARG TARGETARCH
RUN PKG=github.com/ruanun/simple-ai-gateway/internal/buildinfo; \
    CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} go build -trimpath \
    -ldflags "-s -w -X ${PKG}.Version=${VERSION} -X ${PKG}.Commit=${COMMIT} -X ${PKG}.BuildTime=${BUILD_TIME}" \
    -o /out/server ./cmd/server

# ---- 阶段3：运行（alpine：需 ca-certificates 出站 HTTPS 调上游、tzdata 日成本按日期重置）----
FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -g 1000 saig \
    && adduser -D -u 1000 -G saig saig
WORKDIR /app
COPY --from=build /out/server /app/server
COPY LICENSE /app/LICENSE
# data 目录预建并授权（SQLite 默认 DSN=data/saig.db，相对 WORKDIR）
RUN mkdir -p /app/data && chown -R saig:saig /app
USER saig
VOLUME /app/data
EXPOSE 8080
# 存活探针不查 DB/Redis：依赖抖动时不应被编排器反复重启。
# 若改 server.port，需要同步调整这里的端口（或用编排器覆盖 healthcheck）。
HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
    CMD wget -qO- http://127.0.0.1:8080/healthz >/dev/null || exit 1
# config.yaml 缺失/未改默认凭证会被启动护栏拒绝；生产必须用 -v ./config.yaml:/app/config.yaml:ro 挂载自定义配置
ENTRYPOINT ["/app/server"]
