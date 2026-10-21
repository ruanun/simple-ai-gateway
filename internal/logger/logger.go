// Package logger 基于标准库 log/slog 提供全局日志：级别与格式可配，
// 并支持从 ctx 自动带出 request_id 字段（网关排障按请求串联日志）。
// slog.SetDefault 后标准库 log 的输出会自动桥接到同一 handler，
// 因此存量 log.Printf 调用无需改动即可获得统一格式与级别过滤。
package logger

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
)

// ctxKey 非导出，避免与其他包的 ctx 键冲突。
type ctxKey struct{}

// WithRequestID 把请求标识放入 ctx，后续 slog.XxxContext 日志行自动附带 request_id 字段。
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}

// RequestIDFrom 取出 ctx 中的请求标识，未注入时返回空串。
func RequestIDFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(ctxKey{}).(string)
	return id
}

// ctxHandler 包装底层 handler：Handle 时从 ctx 提取 request_id 追加为属性。
type ctxHandler struct{ slog.Handler }

func (h ctxHandler) Handle(ctx context.Context, r slog.Record) error {
	if id := RequestIDFrom(ctx); id != "" {
		r = r.Clone()
		r.AddAttrs(slog.String("request_id", id))
	}
	return h.Handler.Handle(ctx, r)
}

func (h ctxHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return ctxHandler{h.Handler.WithAttrs(attrs)}
}

func (h ctxHandler) WithGroup(name string) slog.Handler {
	return ctxHandler{h.Handler.WithGroup(name)}
}

// New 构造 logger：level 取 debug/info/warn/error（非法值回退 info），
// format 取 text/json（非法值回退 text）。
func New(w io.Writer, level, format string) *slog.Logger {
	var lv slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lv = slog.LevelDebug
	case "warn":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	default:
		lv = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: lv}
	var h slog.Handler
	if strings.ToLower(format) == "json" {
		h = slog.NewJSONHandler(w, opts)
	} else {
		h = slog.NewTextHandler(w, opts)
	}
	return slog.New(ctxHandler{h})
}

// openOutput 解析输出目标：空/"stderr"→stderr、"stdout"→stdout、其他视为文件路径
// （append 打开，进程不轮转，交给 logrotate/容器层）。打开失败回退 stderr 并提示，
// 不中断启动——日志目标错误不应比"没有日志"更致命。
func openOutput(output string) io.Writer {
	switch strings.ToLower(output) {
	case "", "stderr":
		return os.Stderr
	case "stdout":
		return os.Stdout
	}
	f, err := os.OpenFile(output, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "打开日志文件 %s 失败，回退 stderr: %v\n", output, err)
		return os.Stderr
	}
	return f
}

// InitWriter 初始化全局默认日志到指定 writer，标准库 log 输出随之桥接（测试亦用）。
func InitWriter(w io.Writer, level, format string) {
	slog.SetDefault(New(w, level, format))
}

// Fatal 记一条 Error 级日志后退出进程。Init 之后的致命错误必须用它而非标准库 log.Fatalf：
// slog.SetDefault 会把标准库 log 输出恒定桥接为 Info 级，log.level=warn/error 配置下
// log.Fatalf 的消息会被级别过滤丢弃、进程却照常 os.Exit——静默死亡无从排障。
func Fatal(msg string, args ...any) {
	slog.Error(msg, args...)
	os.Exit(1)
}

// Init 按配置初始化全局默认日志；output 见 openOutput（缺省 stderr，与标准库 log 默认一致）。
func Init(level, format, output string) {
	InitWriter(openOutput(output), level, format)
}
