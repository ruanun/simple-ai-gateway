# simple-ai-gateway

[English](README.md) | **简体中文** | [日本語](README.ja.md)

[![CI](https://github.com/ruanun/simple-ai-gateway/actions/workflows/ci.yml/badge.svg?branch=master)](https://github.com/ruanun/simple-ai-gateway/actions/workflows/ci.yml)
[![Security](https://github.com/ruanun/simple-ai-gateway/actions/workflows/security.yml/badge.svg?branch=master)](https://github.com/ruanun/simple-ai-gateway/actions/workflows/security.yml)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)

> 企业级自托管 AI 网关：多协议同协议透传、三层智能路由、渠道多密钥池 + IP 代理容灾、取证级审计回放、精细计费与配额，单二进制零依赖部署。

`simple-ai-gateway` 是一款面向团队与企业的自托管 AI 网关。后端 Go + Gin，默认纯 Go SQLite 驱动（零 cgo），一条命令启动、无需任何外部中间件；前端 React 19 + shadcn/ui，编译为**单个可执行文件**部署（前端静态资源内嵌）。

它专注**「聚合上游 + 计费 + 多租户分发 + 审计」**这一层：把各家官方 API、以及「把订阅转成 API」的上游项目（如 [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI)、[sub2api](https://github.com/Wei-Shaw/sub2api)）统一纳管、计费与分发。订阅接入本身（OAuth 接管、防封）交给那些专业上游——它们把订阅暴露成 OpenAI/Anthropic 兼容端点后，在本网关注册成一条普通渠道即可，可用性由「渠道容灾/降级」天然覆盖。

![落地页](docs/screenshots/landing.png)

## 核心亮点

- **渠道多密钥池 + IP 代理容灾**：渠道下挂多把上游密钥、账号可绑代理组，按优先级 + 权重 + 健康度轮换，账号级 / 代理级独立熔断，「账号 + IP 双通道轮换」分摊速率限制、单点故障自动切换。
- **多协议同协议透传**：OpenAI `/v1/chat/completions`、Anthropic `/v1/messages`、OpenAI Responses `/v1/responses` 入什么协议出什么协议（tool_use / thinking / 流式 SSE 自动兼容），Claude Code / Codex CLI 等原生工具链零改造接入；渠道可选开启 chat↔messages **协议转换兜底**。
- **三层智能路由 + 会话粘连**：渠道 → 账号 → 模型三层路由；别名按优先级分层（故障降级）+ 同层加权随机（负载均衡）；同一会话自动粘连同一账号吃上游 prompt cache，不健康自动逃逸。
- **取证级审计回放**：原始请求 / 响应体异步写入存储层（本地目录或 S3，不进数据库、不阻塞请求链路），请求日志详情中回放完整入参出参（含全文 SHA-256 完整性哈希），请求头脱敏、超期清理、应用级开关。
- **精细计费与配额**：USD 微量整数计价（1 USD = 1,000,000 micros），分项价格表（输入/输出/缓存读/缓存写），models.dev 一键同步官方价；**渠道覆盖价 + 渠道倍率**支持折扣转售场景；应用为配额主体共享团队额度，令牌级日成本上限 / 模型白名单 / 模型映射。
- **使用方门户 + RBAC**：API 使用方自助门户（看配额/建令牌/查日志，支持通用 OIDC 第三方登录）；管理端超管 / 运营 / 只读三角色；顶栏中/英切换（导航、登录、仪表盘、渠道/令牌/应用/价格/别名/日志/设置/渠道健康、门户与调试台等主要页面已迁移；接入文档正文与部分长篇帮助文案仍为中文）。
- **韧性与零依赖**：进程内限流 / 熔断开箱即用；多实例部署时接入 Redis 共享状态并经 Pub/Sub 热更新路由与价格。

## 界面预览

| 仪表盘 | 渠道管理 |
|:---:|:---:|
| ![仪表盘](docs/screenshots/dashboard.png) | ![渠道管理](docs/screenshots/channels.png) |

| 别名路由 | 价格管理 |
|:---:|:---:|
| ![别名路由](docs/screenshots/aliases.png) | ![价格管理](docs/screenshots/prices.png) |

| 请求日志 | 令牌管理 |
|:---:|:---:|
| ![请求日志](docs/screenshots/logs.png) | ![令牌管理](docs/screenshots/api-keys.png) |

| 登录 |  |
|:---:|:---:|
| ![登录](docs/screenshots/login.png) |  |

## 功能总览

| 模块 | 能力 |
| --- | --- |
| 多协议透传 | `/v1/chat/completions`、`/v1/messages`、`/v1/responses` 同协议透传（只替换凭证 + 改写 model）；`/v1/messages/count_tokens`、`/v1/responses/input_tokens` 免费 token 计数；`/v1/embeddings`、`/v1/images/generations`（按次计费）、`/v1/models`；渠道级 chat↔messages 协议转换兜底（恒排在透传候选之后） |
| 智能路由 | 渠道（Channel）→ 账号（Account）→ 模型三层；别名路由（优先级分层 + 加权随机）；真实模型直调；内存路由表热加载；渠道内换账号重试 + 跨渠道降级 |
| 会话粘连 | 同会话粘连同一账号（prompt cache 亲和，5 分钟滑动窗口）；Anthropic 取 `metadata.user_id`、Responses 取 `previous_response_id`、Chat 按客户端指纹派生；不健康自动逃逸 |
| 限流熔断 | API Key 级 + 渠道级 + 账号级 RPM 限流；账号级在途并发上限；渠道 / 账号 / 代理三级独立熔断（失败率窗口 + 429 冷却） |
| 密钥池 | 渠道下挂多把上游密钥（账号）、凭证 AES 加密下沉到账号；优先级 + 权重 + 健康轮换；账号级并发/RPM/过期/临时停用/限流与过载冷却 |
| IP 代理池 | 账号绑定代理组；http / https / socks5 出站；代理加权轮换 + 代理级熔断；与密钥池协同「账号 + IP 双通道轮换」 |
| 计费与配额 | USD 微量整数计价；分项价格表（token 分项 / 按次）；渠道覆盖价 → 全局价两级解析 × 渠道倍率；models.dev 同步官方价；应用配额（402）+ 令牌日成本上限（402，次日重置）；请求前在途额度预留、响应后按实际成本多退少补；用量日 / 时聚合 |
| 幂等与重试 | 非流式 `/v1` 请求支持 `Idempotency-Key`：同 key 同请求体重放缓存响应、不重复计费；不同请求体 409；流式请求明确拒绝 |
| 令牌治理 | 模型映射（请求模型重写）、模型白名单（403）、RPM 限流、标签分类 |
| 审计观测 | 请求日志（协议/渠道/会话/首字延迟/分项 token/费用），详情内嵌取证级审计回放（全量 body 异步写入本地目录 / S3 + 脱敏 + SHA-256 完整性 + 超期清理）；管理端操作日志；渠道健康（成功率/错误率/加权延迟/首 Token）；`/healthz`、`/readyz`、Prometheus `/metrics` |
| 门户与权限 | 使用方门户（自助看配额/建令牌/查日志）；注册开关；通用 OIDC 第三方登录；管理端 RBAC（超管/运营/只读） |
| 错误治理 | 渠道级错误透传开关：全候选失败时可保留最后一个上游状态码与脱敏文案，默认返回不含内部拓扑的公共摘要 |
| 出站治理 | 出站请求头统一治理（白名单回填不伪造 + 自定义请求头护栏 + Anthropic 鉴权头风格 auto/x-api-key/bearer/both） |
| 数据与部署 | SQLite（默认）/ MySQL / PostgreSQL；Redis 可选（多实例共享限流/熔断/粘连 + Pub/Sub 热更新）；单二进制部署；SQLite 在线备份/恢复（`--backup` / `--restore`） |

## 快速开始

**1. 准备配置**（必做）：`jwt.secret`、`security.channel_key_aes_key`、`admin.password` 三项仍为内置默认值或留空时，**进程会拒绝启动**（切断「默认部署即被接管」）。

```bash
cp config.example.yaml config.yaml     # 模板在仓库根，三项标了【必改】
openssl rand -hex 32       # → jwt.secret
openssl rand -hex 16       # → security.channel_key_aes_key（32 hex 字符 = 32 字节）
openssl rand -base64 18    # → admin.password（仅首次播种，之后可在后台改密）
```

**2. 启动**，三选一：

```bash
# 方式一：单二进制（从 Releases 下载对应平台的 tar.gz / zip，或本地构建）
sh scripts/build.sh && ./server -c config.yaml

# 方式二：Docker（镜像支持 linux/amd64 与 linux/arm64）
docker run -d -p 8080:8080 -e TZ=Asia/Shanghai -v saig-data:/app/data \
  -v ./config.yaml:/app/config.yaml:ro \
  ghcr.io/ruanun/simple-ai-gateway:latest

# 方式三：源码运行（Go 1.26+ / pnpm；必须先构建前端，go:embed 依赖 web/dist）
cd web && pnpm install && pnpm build && cd .. && go run ./cmd/server -c config.yaml
```

> Docker Hub 同步发布：`docker.io/ruanun/simple-ai-gateway:latest`，参数与上面的 GHCR 命令相同。GHCR/Docker Hub 镜像与 Releases 归档由 `v*` tag 的 Release 流水线产出；若 `latest` 尚不可用，请用方式一/三本地构建。

浏览器打开 `http://localhost:8080`，用 `admin` + 你在 `admin.password` 里设的口令登录。数据默认落 `data/saig.db`。

配好渠道后一条 curl 验证：

```bash
curl http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer sk-saig-xxxxxxxx" \
  -H "Content-Type: application/json" \
  -d '{"model": "gpt-4o", "messages": [{"role": "user", "content": "你好"}]}'
```

**→ 完整上手路径见 [docs/快速开始.md](docs/快速开始.md)**（启动 → 配渠道 → 配价格 → 建令牌 → 接入 Claude Code / Codex CLI）。

## 部署

- **单实例**：单二进制 + SQLite，无任何外部依赖（限流/熔断/粘连走进程内存实现，功能不降级）。
- **Docker / docker-compose**：仓库根 `docker-compose.yml`；先取消 `./config.yaml:/app/config.yaml:ro` 挂载行的注释（不挂载会因默认凭证被启动护栏拒绝），再 `docker compose up -d`。注意设置 `TZ`（影响日成本限额与用量聚合切日）。
- **反向代理**：透传 `Host` / `X-Forwarded-Proto`（OIDC 回调地址在系统设置里显式填写），对 `/v1/*` 关闭响应缓冲（SSE 不攒包），并在 `config.yaml` 的 `server.trusted_proxies` 填上代理地址（否则网关忽略 `X-Forwarded-For`，全站共用一个登录限流桶）。
- **多实例**：MySQL/PG + Redis（共享限流/熔断/粘连，Pub/Sub 广播路由与价格热更新）。

**→ systemd / Nginx 示例、配置完整参考、备份与安全加固清单见 [docs/部署指南.md](docs/部署指南.md)**。

## 协议接入

| 入站端点 | 客户端协议 | 出站方式 | 适用工具 |
| --- | --- | --- | --- |
| `POST /v1/chat/completions` | OpenAI Chat | 透传到 `type=openai` 渠道 | Cursor、Continue、通用 OpenAI SDK |
| `POST /v1/messages` | Anthropic Messages | 透传到 `type=anthropic` 渠道（支持 `x-api-key` / `Bearer`） | Claude Code、Claude SDK |
| `POST /v1/responses` | OpenAI Responses | 透传到 `type=openai` 渠道 | Codex CLI、OpenAI Agents SDK |
| `POST /v1/embeddings` | OpenAI Embeddings | 转发 | 嵌入调用 |
| `POST /v1/images/generations` | OpenAI Images | 转发（按次计费） | 图像生成 |
| `GET /v1/models` | OpenAI Models | 本地模型列表 | — |

> **一渠道一协议**：渠道协议即 `Channel.Type`，上游同时提供两种协议端点时各建一条渠道。开启渠道「协议转换」后，异协议请求可经 chat↔messages 转换由该渠道兜底（转换候选恒排在同协议透传候选之后）。

管理后台内置「接入文档」页，含各工具（Claude Code / Codex CLI / Cursor 等）的详细接入步骤，可直接发给使用方。

## 技术栈

- **后端**：Go 1.26 + Gin + GORM（`glebarez/sqlite` 纯 Go 驱动，零 cgo）；JWT 鉴权；AES 加密上游凭证
- **前端**：React 19 + Vite + TypeScript；Tailwind CSS v4 + shadcn/ui（Radix 内核）；TanStack Query；recharts
- **设计**：主蓝 `#1E40AF` + 强调琥珀 `#D97706`，亮 / 暗双主题

## 目录结构

```
simple-ai-gateway/
├── cmd/server/            # 程序入口
├── internal/
│   ├── config/            # 配置加载（yaml + 默认值）
│   ├── db/ · model/       # 数据库连接、GORM 模型与迁移（按 ID 幂等执行）
│   ├── domain/            # 业务领域
│   │   ├── gateway/       #   网关核心：多端点编排 / 派发 / 计费 / 选密钥
│   │   ├── channel/ account/ alias/ apikey/ app/ price/ proxy/
│   │   ├── requestlog/ usage/ keytag/ setting/ cleanup/
│   ├── router/            # 内存路由表（热加载，Pub/Sub 重建）
│   ├── convert/ v1proto/  # chat↔messages 转换、/v1 双协议错误信封
│   ├── ratelimit/ breaker/ session/   # 限流 / 熔断 / 会话粘连（内存与 Redis 双实现）
│   ├── accountpick/ proxypick/ proxydial/  # 密钥池与代理选择、代理拨号
│   ├── audit/             # 取证级审计（异步写 / 回放 / 清理）
│   ├── storage/           # 审计正文存储抽象层（local / s3）
│   ├── billing/ backup/ ipgeo/ crypto/ redis/
│   ├── middleware/ admin/ portal/     # 鉴权限流中间件、管理面、使用方门户 API
│   └── server/            # 唯一依赖装配点：路由注册 + 前端内嵌
├── web/                   # React 前端（管理后台 + 门户 + 落地页）
├── tools/                 # 本地联调进程（mock 上游 / 测试代理）
├── scripts/build.sh       # 前端 → 后端顺序构建
└── docs/                  # 文档与设计资料
```

## 配置说明

配置文件由 `-c` 指定，缺省读当前目录的 `config.yaml`；未列出的项走内置默认值。标【必改】的三项留默认或留空会**拒绝启动**：

| 配置项 | 说明 | 默认值 |
| --- | --- | --- |
| `server.port` | 监听端口 | `8080` |
| `server.trusted_proxies` | 可信反向代理 IP/CIDR；为空时只信 TCP 对端 | `[]` |
| `database.driver` / `dsn` | `sqlite` / `mysql` / `postgres` 与连接串 | `sqlite` / `data/saig.db` |
| `database.max_open_conns` 等 | 连接池参数 | 驱动默认 |
| `redis.enabled` / `addr` | 多实例共享状态（单实例无需开启） | `false` |
| `admin.username` / `password` | 超管账号（仅首次启动播种，之后后台改密） | `admin` / **【必改】** |
| `jwt.secret` / `expire_hours` | 管理后台 JWT 签名密钥 | **【必改】** / `24` |
| `security.apikey_prefix` | 业务令牌前缀 | `sk-saig` |
| `security.channel_key_aes_key` | 上游凭证 AES 密钥，**长度须 16/24/32 字节**，否则拒绝启动 | **【必改】** |
| `log.level` / `format` / `output` | 进程日志：级别、text/json、stderr/stdout/文件 | `info` / `text` / `stderr` |

完整模板见仓库根 `config.example.yaml`；常用项说明见 [docs/部署指南.md](docs/部署指南.md#配置参考)。

## 文档

| 文档 | 内容 |
| --- | --- |
| [docs/快速开始.md](docs/快速开始.md) | 10 分钟跑通：启动 → 配渠道 → 第一个请求 → 接入工具 |
| [docs/部署指南.md](docs/部署指南.md) | 二进制 / Docker / 反向代理 / 多实例 / 备份 / 安全加固 |
| [docs/使用手册.md](docs/使用手册.md) | 核心概念、管理后台逐页详解、使用方门户、常见问题 |
| [docs/UI设计规范.md](docs/UI设计规范.md) | 前端配色与组件约定 |

## 作者与许可

- 作者：**ruanun**
- 许可：[Apache License 2.0](LICENSE)
- 贡献：请先阅读 [CONTRIBUTING.md](CONTRIBUTING.md)，尤其是提交邮箱约定
- 安全披露：请勿在公开 issue 中贴出凭证或未修复漏洞，按 [SECURITY.md](SECURITY.md) 私密报告
