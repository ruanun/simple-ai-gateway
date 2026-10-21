#!/usr/bin/env sh
# 本地构建脚本：固化「先前端后后端」顺序（web/dist 是 go:embed 的输入，缺失则 go build 直接编译失败）。
# 用法：      sh scripts/build.sh
# 交叉编译：  GOOS=linux GOARCH=arm64 sh scripts/build.sh
# 注入版本：  VERSION=v1.0.0 sh scripts/build.sh
set -e
cd "$(dirname "$0")/.."

echo "==> 构建前端 (web/dist)"
(cd web && pnpm install --frozen-lockfile && pnpm build)

VERSION="${VERSION:-dev}"
# 构建元数据：可用环境变量覆盖，否则从 git / 系统时间取；git 不可用时回退 unknown。
COMMIT="${COMMIT:-$(git rev-parse --short=12 HEAD 2>/dev/null || echo unknown)}"
# 工作区有未提交改动时给提交号打脏标记，让产物来源诚实。
if [ "$COMMIT" != "unknown" ] && ! git diff --quiet HEAD 2>/dev/null; then
	COMMIT="${COMMIT}-dirty"
fi
BUILD_TIME="${BUILD_TIME:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}"
TARGET_OS="${GOOS:-$(go env GOOS)}"
OUT="server"
[ "$TARGET_OS" = "windows" ] && OUT="server.exe"

PKG="github.com/ruanun/simple-ai-gateway/internal/buildinfo"
echo "==> 构建后端 -> ${OUT} (version=${VERSION}, commit=${COMMIT}, ${TARGET_OS}/${GOARCH:-$(go env GOARCH)})"
CGO_ENABLED=0 go build -trimpath \
	-ldflags "-s -w -X ${PKG}.Version=${VERSION} -X ${PKG}.Commit=${COMMIT} -X ${PKG}.BuildTime=${BUILD_TIME}" \
	-o "${OUT}" ./cmd/server
echo "==> 完成: ${OUT}"
