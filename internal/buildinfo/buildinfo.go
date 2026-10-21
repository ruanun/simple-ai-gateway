// Package buildinfo 汇集构建期注入的版本元数据，供启动横幅、日志与 --version 输出统一取用。
//
// Version/Commit/BuildTime 三项由打包脚本经 -ldflags "-X ...=" 注入
// （见 scripts/build.sh、Dockerfile、.github/workflows/release.yml）；
// 若未注入（如开发时直接 `go build ./cmd/server`），init 会回退到 Go 工具链
// 自动写入的 VCS 戳记补齐 commit / 构建时间 / 源码树是否有未提交改动。
// 注意 Docker 构建上下文已排除 .git（见 .dockerignore），故镜像内 VCS 回退不可用，
// 必须依赖 ldflags 注入，这也是打包链路显式传参的原因。
package buildinfo

import (
	"fmt"
	"runtime"
	"runtime/debug"
)

// 以下三项由构建期 ldflags 注入；变量名即注入目标，保持稳定勿改（打包脚本按包全名引用）。
var (
	// Version 发布版本号（如 v1.2.3）；本地未注入时为 dev。
	Version = "dev"
	// Commit 构建时的 git 提交号（短号）。
	Commit = ""
	// BuildTime 构建时刻，RFC3339 UTC（如 2026-07-17T10:00:00Z）。
	BuildTime = ""
)

// 以下两项运行期天然可得，无需注入。
var (
	// GoVersion 编译所用 Go 工具链版本。
	GoVersion = runtime.Version()
	// Platform 目标平台，形如 linux/amd64。
	Platform = runtime.GOOS + "/" + runtime.GOARCH
)

// dirty 标记构建时源码树是否存在未提交改动（仅 VCS 回退路径能判定）。
var dirty bool

func init() {
	// Commit 非空说明已由 ldflags 注入，脏标记应由注入方（打包脚本）负责，
	// 此处不再从 VCS 回退，避免与打包脚本自带的 -dirty 叠成双标记。
	injected := Commit != ""
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return
	}
	// 未经 ldflags 注入时，用 Go 自动写入的 VCS 戳记回退填充，保证裸 `go build` 也带信息。
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			if Commit == "" {
				Commit = shortCommit(s.Value)
			}
		case "vcs.time":
			if BuildTime == "" {
				BuildTime = s.Value
			}
		case "vcs.modified":
			if !injected {
				dirty = s.Value == "true"
			}
		}
	}
}

// shortCommit 截取 12 位短提交号。
func shortCommit(c string) string {
	if len(c) > 12 {
		return c[:12]
	}
	return c
}

// commit 返回带脏标记、缺失回退 unknown 的提交号。
func commit() string {
	c := Commit
	if c == "" {
		c = "unknown"
	}
	if dirty {
		c += "-dirty"
	}
	return c
}

// built 返回构建时间，缺失时回退 unknown。
func built() string {
	if BuildTime == "" {
		return "unknown"
	}
	return BuildTime
}

// Fields 返回结构化日志用的键值对（配合 slog 的可变参数展开）。
func Fields() []any {
	return []any{
		"version", Version,
		"commit", commit(),
		"built", built(),
		"go", GoVersion,
		"platform", Platform,
	}
}

// String 返回单行摘要，用于 --version 输出与启动横幅。
func String() string {
	return fmt.Sprintf("simple-ai-gateway %s (commit %s, built %s, %s, %s)",
		Version, commit(), built(), GoVersion, Platform)
}
