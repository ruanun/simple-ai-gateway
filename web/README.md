# simple-ai-gateway web

管理后台、使用方门户与落地页前端。技术栈：React 19 + Vite + TypeScript + Tailwind CSS v4 + shadcn/ui（Radix 内核）。

## 开发

```bash
pnpm install
pnpm dev        # http://localhost:5173；/admin 与 /v1 已代理到后端 :8080
pnpm lint
pnpm test       # vitest
pnpm build      # 产出 dist/，后端 go:embed 依赖它
```

先启动后端（仓库根 `go run ./cmd/server -c config.yaml`），再跑前端开发服务器。生产构建后由 Go 单二进制内嵌 `web/dist`，不需要单独部署前端。

## 目录

- `src/pages/`：管理端/门户/落地页
- `src/components/`：业务组件与 `ui/` 下的 shadcn 组件
- `src/lib/`：API、i18n、格式化、类型定义
