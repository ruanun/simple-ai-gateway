#!/usr/bin/env sh
# docker compose 构建包装：自动算好构建元数据再调 compose，避免镜像里版本信息全是 unknown。
#
# 为什么需要它：.dockerignore 排除了 .git，镜像内拿不到 VCS 戳记（这是 buildinfo 三档降级里
# 唯一能兜底的一档），所以 Docker 场景下版本信息**只能**靠 ldflags 注入。而 compose 不会自己
# 去跑 git，裸跑 `docker compose build` 只会落到 Dockerfile 的默认值 dev/unknown/unknown，
# 启动横幅与 --version 就核对不出线上跑的是哪份代码。
#
# 用法：      sh scripts/docker-build.sh
# 指定版本：  VERSION=v1.0.0 sh scripts/docker-build.sh
# 附加参数：  sh scripts/docker-build.sh --no-cache
#
# 元数据口径与 scripts/build.sh 保持一致（含工作区脏标记）。
set -e
cd "$(dirname "$0")/.."

VERSION="${VERSION:-dev}"
COMMIT="${COMMIT:-$(git rev-parse --short=12 HEAD 2>/dev/null || echo unknown)}"
# 工作区有未提交改动时给提交号打脏标记，让产物来源诚实。
if [ "$COMMIT" != "unknown" ] && ! git diff --quiet HEAD 2>/dev/null; then
	COMMIT="${COMMIT}-dirty"
fi
BUILD_TIME="${BUILD_TIME:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}"

echo "==> docker compose build (version=${VERSION}, commit=${COMMIT}, built=${BUILD_TIME})"
VERSION="$VERSION" COMMIT="$COMMIT" BUILD_TIME="$BUILD_TIME" \
	docker compose build "$@"
# 核对命令不要再写 ./server：Dockerfile 的 ENTRYPOINT 已是 /app/server，重复写会让
# --version 变成位置参数之后的普通参数而不被 flag 解析，进而走正常启动流程。
echo "==> 完成。核对镜像版本： docker compose run --rm saig --version"
