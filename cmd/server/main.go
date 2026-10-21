package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/ruanun/simple-ai-gateway/internal/backup"
	"github.com/ruanun/simple-ai-gateway/internal/buildinfo"
	"github.com/ruanun/simple-ai-gateway/internal/config"
	"github.com/ruanun/simple-ai-gateway/internal/db"
	"github.com/ruanun/simple-ai-gateway/internal/logger"
	"github.com/ruanun/simple-ai-gateway/internal/model"
	saigredis "github.com/ruanun/simple-ai-gateway/internal/redis"
	"github.com/ruanun/simple-ai-gateway/internal/server"
)

func main() {
	cfgPath := flag.String("c", "", "config file path")
	showVersion := flag.Bool("version", false, "打印版本与构建信息后退出")
	backupOut := flag.String("backup", "", "备份 SQLite 数据库与本地审计目录到指定目录后退出")
	restoreSrc := flag.String("restore", "", "从备份文件/目录恢复 SQLite 数据库后退出（须先停止网关）")
	flag.BoolVar(showVersion, "v", false, "打印版本与构建信息后退出（--version 简写）")
	flag.Parse()
	if *showVersion {
		// 运维核对二进制来源用：不加载配置、不建库，直接打印后退出。
		fmt.Println(buildinfo.String())
		return
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		// 配置读取/解析失败一律拒绝启动，避免无声回退到默认口令/密钥。
		log.Fatalf("load config: %v", err)
	}
	// 尽早初始化全局日志（级别/格式/输出目标），此后标准库 log 输出统一桥接到 slog。
	logger.Init(cfg.Log.Level, cfg.Log.Format, cfg.Log.Output)
	if *backupOut != "" && *restoreSrc != "" {
		log.Fatalf("--backup 与 --restore 不能同时使用")
	}
	if *backupOut != "" {
		path, err := backup.Create(cfg, *backupOut)
		if err != nil {
			log.Fatalf("备份失败: %v", err)
		}
		fmt.Println("备份完成:", path)
		return
	}
	if *restoreSrc != "" {
		if err := backup.Restore(cfg, *restoreSrc); err != nil {
			log.Fatalf("恢复失败: %v", err)
		}
		fmt.Println("恢复完成:", *restoreSrc)
		return
	}
	// 尽早打印构建信息（版本/提交/构建时间/Go/平台），便于运维核对线上二进制来源；
	// 放在护栏/建库之前，即使后续启动失败也已留下这条记录。
	slog.Info("构建信息", buildinfo.Fields()...)
	// gin 模式联动日志级别：debug 级视为开发场景保留 GIN-debug 路由表输出，
	// 其余级别切 release 压掉启动期噪音（测试不受影响，各测试自设 TestMode）。
	if !strings.EqualFold(cfg.Log.Level, "debug") {
		gin.SetMode(gin.ReleaseMode)
	}
	// 安全启动护栏（Critical #1/#2）：仍在使用内置默认凭证或空值时拒绝对外启动，
	// 切断「默认部署即被接管」链路。请在 config.yaml 覆盖 jwt.secret /
	// security.channel_key_aes_key / admin.password 后重启（生成参考见 docs/部署指南.md）。
	if issues := cfg.CheckProductionSafety(); len(issues) > 0 {
		logger.Fatal("拒绝以不安全的默认凭证启动，请在配置中覆盖后重启", "issues", strings.Join(issues, "; "))
	}
	// 非法配置拒绝启动：AES key 长度必须为 16/24/32 字节，否则渠道密钥加解密会在运行期失败。
	switch len(cfg.Security.ChannelKeyAESKey) {
	case 16, 24, 32:
	default:
		logger.Fatal("security.channel_key_aes_key 长度非法，必须为 16/24/32 字节", "length", len(cfg.Security.ChannelKeyAESKey))
	}
	g, err := db.Open(cfg.Database)
	if err != nil {
		logger.Fatal("打开数据库失败", "err", err)
	}
	if err := db.Migrate(g, model.Migrations(cfg.Admin.Username, cfg.Admin.Password)); err != nil {
		logger.Fatal("数据库迁移失败", "err", err)
	}
	redisCli := saigredis.NewClient(cfg.Redis)
	r, shutdown, err := server.NewWithShutdown(cfg, g, redisCli)
	if err != nil {
		logger.Fatal("初始化服务失败", "err", err)
	}
	addr := fmt.Sprintf(":%d", cfg.Server.Port)
	// 连接级加固：ReadHeaderTimeout 防 slowloris（慢速发送请求头长期占用连接/goroutine），
	// IdleTimeout 回收空闲 keep-alive，MaxHeaderBytes 限制头大小。
	// 刻意不设 ReadTimeout/WriteTimeout：流式 SSE 为长连接，整体读写期限会截断正常长响应。
	srv := &http.Server{
		Addr:              addr,
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20, // 1 MiB
	}
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Fatal("监听失败", "err", err)
		}
	}()
	slog.Info("simple-ai-gateway 已启动", "version", buildinfo.Version, "addr", addr)

	// 优雅关闭：收到 SIGINT/SIGTERM 后先停止接收新请求、等在途请求完成，
	// 再停后台定时任务并 flush 审计队列，避免退出时丢审计、泄漏 ticker(E5)。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	slog.Info("正在优雅关闭...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Warn("http server 关闭超时", "err", err)
	}
	shutdown()
	slog.Info("已退出")
}
