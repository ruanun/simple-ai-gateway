package logger

import (
	"bytes"
	"context"
	"encoding/json"
	stdlog "log"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLevelFilter level=warn 时 Info 被过滤、Warn 正常输出。
func TestLevelFilter(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, "warn", "text")
	l.Info("信息日志")
	if buf.Len() != 0 {
		t.Fatalf("level=warn 时 Info 不应输出，得到: %s", buf.String())
	}
	l.Warn("警告日志")
	if !strings.Contains(buf.String(), "警告日志") {
		t.Fatalf("Warn 应输出，得到: %s", buf.String())
	}
}

// TestJSONFormat format=json 时输出为合法 JSON 且含 msg/level 字段。
func TestJSONFormat(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, "info", "json")
	l.Info("json 日志", slog.Int("channel", 7))
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("输出应为合法 JSON: %v, 得到: %s", err, buf.String())
	}
	if m["msg"] != "json 日志" {
		t.Fatalf("msg 字段不符: %v", m["msg"])
	}
	if m["channel"] != float64(7) {
		t.Fatalf("channel 字段不符: %v", m["channel"])
	}
}

// TestInvalidFallback 非法 level/format 回退 info/text。
func TestInvalidFallback(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, "bogus", "bogus")
	l.Debug("调试日志")
	if buf.Len() != 0 {
		t.Fatalf("回退 info 级后 Debug 不应输出，得到: %s", buf.String())
	}
	l.Info("信息日志")
	out := buf.String()
	if !strings.Contains(out, "信息日志") {
		t.Fatalf("回退 info 级后 Info 应输出，得到: %s", out)
	}
	if strings.HasPrefix(strings.TrimSpace(out), "{") {
		t.Fatalf("回退 text 格式后不应输出 JSON: %s", out)
	}
}

// TestRequestIDFromContext ctx 经 WithRequestID 注入后，XxxContext 日志行自动带 request_id 字段。
func TestRequestIDFromContext(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, "info", "json")
	ctx := WithRequestID(context.Background(), "abc123")
	l.InfoContext(ctx, "带请求标识")
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("解析输出失败: %v, 得到: %s", err, buf.String())
	}
	if m["request_id"] != "abc123" {
		t.Fatalf("request_id 字段不符: %v, 完整输出: %s", m["request_id"], buf.String())
	}
}

// TestNoRequestIDNoField ctx 未注入 request_id 时不产生空字段。
func TestNoRequestIDNoField(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, "info", "json")
	l.InfoContext(context.Background(), "无请求标识")
	if strings.Contains(buf.String(), "request_id") {
		t.Fatalf("未注入时不应出现 request_id 字段: %s", buf.String())
	}
}

// TestWithAttrsGroupPassthrough 包装 handler 需正确透传 WithAttrs/WithGroup（logger.With 场景）。
func TestWithAttrsGroupPassthrough(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, "info", "json").With(slog.String("component", "probe"))
	ctx := WithRequestID(context.Background(), "xyz")
	l.InfoContext(ctx, "组合字段")
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("解析输出失败: %v, 得到: %s", err, buf.String())
	}
	if m["component"] != "probe" || m["request_id"] != "xyz" {
		t.Fatalf("With 字段与 request_id 应同时存在: %s", buf.String())
	}
}

// TestOpenOutputStdStreams 空值/"stderr" 解析为 stderr，"stdout" 解析为 stdout。
func TestOpenOutputStdStreams(t *testing.T) {
	if openOutput("") != os.Stderr {
		t.Fatal(`空值应解析为 stderr`)
	}
	if openOutput("stderr") != os.Stderr {
		t.Fatal(`"stderr" 应解析为 stderr`)
	}
	if openOutput("stdout") != os.Stdout {
		t.Fatal(`"stdout" 应解析为 stdout`)
	}
}

// TestOpenOutputFile 文件路径按 append 打开，日志内容可读回。
func TestOpenOutputFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "saig.log")
	w := openOutput(p)
	// 生产上句柄持有到进程退出；测试须关闭，否则 Windows 下 TempDir 清理失败。
	if f, ok := w.(*os.File); ok {
		t.Cleanup(func() { _ = f.Close() })
	}
	l := New(w, "info", "text")
	l.Info("写入文件")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("读回日志文件失败: %v", err)
	}
	if !strings.Contains(string(b), "写入文件") {
		t.Fatalf("日志文件内容不符: %s", b)
	}
}

// TestOpenOutputBadPathFallsBack 无法打开的路径回退 stderr，不中断启动。
func TestOpenOutputBadPathFallsBack(t *testing.T) {
	p := filepath.Join(t.TempDir(), "不存在的子目录", "saig.log")
	if openOutput(p) != os.Stderr {
		t.Fatal("打开失败应回退 stderr")
	}
}

// TestInitBridgesStdlog Init 后标准库 log 输出桥接到 slog handler（Go 1.21+ SetDefault 行为）。
func TestInitBridgesStdlog(t *testing.T) {
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	var buf bytes.Buffer
	InitWriter(&buf, "info", "json")
	// 标准库 log 的默认输出应被 slog.SetDefault 接管
	stdlog.Print("桥接测试")
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("标准库 log 输出应桥接为 JSON: %v, 得到: %s", err, buf.String())
	}
	if !strings.Contains(m["msg"].(string), "桥接测试") {
		t.Fatalf("桥接内容不符: %s", buf.String())
	}
}
