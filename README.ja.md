# simple-ai-gateway

[English](README.md) | [简体中文](README.zh-CN.md) | **日本語**

[![CI](https://github.com/ruanun/simple-ai-gateway/actions/workflows/ci.yml/badge.svg?branch=master)](https://github.com/ruanun/simple-ai-gateway/actions/workflows/ci.yml)
[![Security](https://github.com/ruanun/simple-ai-gateway/actions/workflows/security.yml/badge.svg?branch=master)](https://github.com/ruanun/simple-ai-gateway/actions/workflows/security.yml)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)

> エンタープライズ向けセルフホスト AI ゲートウェイ：複数プロトコルの同一プロトコル透過、3 層インテリジェントルーティング、チャネル多キープール + IP プロキシフェイルオーバー、フォレンジック監査リプレイ、詳細な課金とクォータを、デフォルトで外部依存のない単一バイナリとして提供します。

`simple-ai-gateway` は、チームや企業向けのセルフホスト型 AI ゲートウェイです。バックエンドは Go + Gin、デフォルトで純 Go の SQLite ドライバ（cgo なし）を使用するため、コマンド 1 つで起動でき、外部ミドルウェアは不要です。フロントエンドは React 19 + shadcn/ui で、**単一の実行ファイル**に埋め込んでデプロイします（静的アセットはバイナリに埋め込み）。

**「上流の集約 + 課金 + マルチテナント配信 + 監査」**に特化しています。各社の公式 API や、サブスクリプションを API 化する上流プロジェクト（[CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI)、[sub2api](https://github.com/Wei-Shaw/sub2api) など）を一元管理し、課金・配信します。サブスクリプション接続そのもの（OAuth 接続の引き継ぎ、BAN 回避）は専門の上流プロジェクトに任せます。それらがサブスクリプションを OpenAI/Anthropic 互換エンドポイントとして公開したら、本ゲートウェイに通常のチャネルとして登録するだけです。可用性は「チャネルのフェイルオーバー/デグレード」でカバーされます。

> `docs/` 以下の詳細ドキュメント、`CONTRIBUTING.md`、`SECURITY.md` は現在簡体字中国語のみです。

![ランディングページ](docs/screenshots/landing.png)

## ハイライト

- **チャネル多キープール + IP プロキシフェイルオーバー**：1 つのチャネルに複数の上流キーを登録し、アカウントをプロキシグループにバインドできます。優先度 + 重み + ヘルスに基づいてローテーションし、アカウント単位 / プロキシ単位で独立したサーキットブレーカーを持ちます。「アカウント + IP の二重ローテーション」でレート制限を分散し、単一障害点を自動的に切り離します。
- **マルチプロトコル同一プロトコル透過**：OpenAI `/v1/chat/completions`、Anthropic `/v1/messages`、OpenAI Responses `/v1/responses` は、受信したプロトコルのまま上流へ透過します（tool_use / thinking / ストリーミング SSE に自動対応）。Claude Code / Codex CLI などのネイティブツールチェーンを変更なしで接続できます。チャネル単位で chat↔messages の**プロトコル変換フォールバック**を任意で有効化できます。
- **3 層インテリジェントルーティング + セッションアフィニティ**：チャネル → アカウント → モデルの 3 層ルーティング。エイリアスは優先度で階層化（フェイルオーバー）し、同一階層内では重み付きランダム（負荷分散）で選択します。同一セッションは同じアカウントに固定され、上流の prompt cache を活用します。不健全なアカウントからは自動的に逃避します。
- **フォレンジック監査リプレイ**：生のリクエスト / レスポンス本文をストレージ層（ローカルディレクトリまたは S3）へ非同期で書き込みます（DB には保存せず、リクエスト経路をブロックしません）。リクエストログの詳細で完全な入出力をリプレイでき、本文全体の SHA-256 整合性ハッシュ、ヘッダーのマスキング、保持期限切れのクリーンアップ、アプリ単位のスイッチを備えます。
- **詳細な課金とクォータ**：USD マイクロ整数で価格を管理（1 USD = 1,000,000 micros）。項目別価格表（入力 / 出力 / キャッシュ読み取り / キャッシュ書き込み）、models.dev からの公式価格ワンクリック同期、**チャネル上書き価格 + チャネル倍率**による割引再販に対応します。アプリをクォータ主体としてチーム予算を共有し、キー単位の日次コスト上限 / モデル許可リスト / モデルマッピングを設定できます。
- **利用者ポータル + RBAC**：API 利用者向けのセルフサービス・ポータル（クォータ確認 / キー作成 / ログ検索、汎用 OIDC サードパーティログイン対応）。管理コンソールはスーパー管理者 / オペレーター / 読み取り専用の 3 ロール。トップバーで中国語 / 英語を切り替えられます（ナビゲーション、ログイン、ダッシュボード、チャネル / キー / アプリ / 価格 / エイリアス / ログ / 設定 / チャネルヘルス、ポータル、プレイグラウンドなどの主要ページは移行済み。接続ドキュメント本文と一部の長文ヘルプは中国語のままです）。
- **耐障害性とゼロ依存**：プロセス内のレート制限 / サーキットブレーカーはすぐに利用できます。マルチインスタンス構成では Redis で状態を共有し、Pub/Sub でルートと価格をホットリロードできます。

## UI プレビュー

| ダッシュボード | チャネル管理 |
|:---:|:---:|
| ![ダッシュボード](docs/screenshots/dashboard.png) | ![チャネル管理](docs/screenshots/channels.png) |

| エイリアスルーティング | 価格管理 |
|:---:|:---:|
| ![エイリアスルーティング](docs/screenshots/aliases.png) | ![価格管理](docs/screenshots/prices.png) |

| リクエストログ | API キー |
|:---:|:---:|
| ![リクエストログ](docs/screenshots/logs.png) | ![API キー](docs/screenshots/api-keys.png) |

| ログイン |  |
|:---:|:---:|
| ![ログイン](docs/screenshots/login.png) |  |

## 機能一覧

| モジュール | 機能 |
| --- | --- |
| マルチプロトコル透過 | `/v1/chat/completions`、`/v1/messages`、`/v1/responses` を同一プロトコルで透過（認証情報の置換と model の書き換えのみ）。`/v1/messages/count_tokens`、`/v1/responses/input_tokens` の無料トークンカウント。`/v1/embeddings`、`/v1/images/generations`（呼び出しごとの課金）、`/v1/models`。チャネル単位の chat↔messages 変換フォールバック（常に同一プロトコル透過の候補より後） |
| インテリジェントルーティング | チャネル → アカウント → モデルの 3 層。エイリアスルーティング（優先度階層 + 重み付きランダム）、実モデルへの直接ルーティング、インメモリルートテーブルのホットリロード、チャネル内のアカウント切替リトライ + チャネル間デグレード |
| セッションアフィニティ | 同一セッションを同じアカウントに固定（prompt cache 親和性、5 分間のスライディングウィンドウ）。Anthropic は `metadata.user_id`、Responses は `previous_response_id`、Chat はクライアントフィンガープリントから導出。不健全時は自動的に逃避 |
| レート制限とサーキットブレーカー | API キー単位 + チャネル単位 + アカウント単位の RPM 制限。アカウント単位の同時実行数上限。チャネル / アカウント / プロキシの 3 段階の独立サーキットブレーカー（失敗率ウィンドウ + 429 クールダウン） |
| キープール | 1 つのチャネルに複数の上流キー（アカウント）を登録。認証情報は AES で暗号化してアカウントに保存。優先度 + 重み + ヘルスでローテーション。アカウント単位の同時実行 / RPM / 有効期限 / 一時停止 / レート制限・過負荷クールダウン |
| IP プロキシプール | アカウントをプロキシグループにバインド。HTTP / HTTPS / SOCKS5 でアウトバウンド。プロキシの重み付きローテーション + プロキシ単位のサーキットブレーカー。キープールと連携した「アカウント + IP の二重ローテーション」 |
| 課金とクォータ | USD マイクロ整数価格。項目別価格表（トークン項目 / 呼び出しごと）。チャネル上書き価格 → グローバル価格の 2 段階解決 × チャネル倍率。models.dev の公式価格同期。アプリクォータ（402）+ キー単位の日次コスト上限（402、翌日リセット）。リクエスト前のクォータ予約（処理中分）、レスポンス後の実コスト精算。日次 / 時間次集計 |
| 冪等性とリトライ | 非ストリーミングの `/v1` リクエストは `Idempotency-Key` に対応。同じキー + 同じ本文ならキャッシュ済みレスポンスを返し、二重課金しない。本文が異なる場合は 409。ストリーミングは明示的に拒否 |
| キーガバナンス | モデルマッピング（リクエスト model の書き換え）、モデル許可リスト（403）、RPM 制限、タグ分類 |
| 監査と可観測性 | リクエストログ（プロトコル / チャネル / セッション / 最初のトークンまでのレイテンシ / トークン内訳 / コスト）。詳細にはフォレンジック監査リプレイを埋め込み（本文全体をローカルディレクトリ / S3 へ非同期保存 + マスキング + SHA-256 整合性 + 保持期限切れのクリーンアップ）。管理操作ログ。チャネルヘルス（成功率 / エラー率 / 加重平均レイテンシ / 最初のトークン）。`/healthz`、`/readyz`、Prometheus `/metrics` |
| ポータルと権限 | 利用者ポータル（クォータ確認 / キー作成 / ログ検索のセルフサービス）。登録スイッチ。汎用 OIDC サードパーティログイン。管理コンソールの RBAC（スーパー管理者 / オペレーター / 読み取り専用） |
| エラーガバナンス | チャネル単位のエラー透過スイッチ。全候補失敗時に最後の上流ステータスコードとマスキング済みメッセージを保持できます。デフォルトでは内部トポロジを含まない公開サマリーを返します |
| アウトバウンドガバナンス | アウトバウンドヘッダーの一元管理（許可リストのバックフィルで偽装しない + カスタムヘッダーのガード + Anthropic 認証ヘッダースタイル auto/x-api-key/bearer/both） |
| データとデプロイ | SQLite（デフォルト）/ MySQL / PostgreSQL。Redis は任意（マルチインスタンスでのレート制限 / サーキットブレーカー / セッションアフィニティ共有 + Pub/Sub ホットリロード）。単一バイナリでデプロイ。SQLite のオンラインバックアップ / リストア（`--backup` / `--restore`） |

## クイックスタート

**1. 設定を準備する（必須）**。`jwt.secret`、`security.channel_key_aes_key`、`admin.password` の 3 項目が組み込みデフォルトのまま、または空の場合、**プロセスは起動を拒否します**（デフォルト構成のまま乗っ取られるのを防ぐため）。

```bash
cp config.example.yaml config.yaml     # テンプレートはリポジトリ直下。3 項目は【必須】
openssl rand -hex 32       # → jwt.secret
openssl rand -hex 16       # → security.channel_key_aes_key（32 hex 文字 = 32 バイト）
openssl rand -base64 18    # → admin.password（初回起動時のみシード。後で管理画面から変更可能）
```

**2. 起動する**（いずれか 1 つ）:

```bash
# オプション 1：単一バイナリ（Releases からプラットフォーム別の tar.gz / zip をダウンロード、またはローカルビルド）
sh scripts/build.sh && ./server -c config.yaml

# オプション 2：Docker（linux/amd64 と linux/arm64 のマルチアーキテクチャ）
docker run -d -p 8080:8080 -e TZ=Asia/Shanghai -v saig-data:/app/data \
  -v ./config.yaml:/app/config.yaml:ro \
  ghcr.io/ruanun/simple-ai-gateway:latest

# オプション 3：ソースから実行（Go 1.26+ / pnpm。go:embed のため先にフロントエンドをビルド）
cd web && pnpm install && pnpm build && cd .. && go run ./cmd/server -c config.yaml
```

> Docker Hub ミラー：`docker.io/ruanun/simple-ai-gateway:latest`。引数は上記の GHCR コマンドと同じです。GHCR / Docker Hub イメージと Releases アーカイブは `v*` タグの Release ワークフローで生成されます。`latest` がまだ利用できない場合は、オプション 1 または 3 でローカルビルドしてください。

ブラウザで `http://localhost:8080` を開き、`admin.password` に設定したパスワードで `admin` としてログインします。データはデフォルトで `data/saig.db` に保存されます。

チャネル設定後、curl で疎通確認できます:

```bash
curl http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer sk-saig-xxxxxxxx" \
  -H "Content-Type: application/json" \
  -d '{"model": "gpt-4o", "messages": [{"role": "user", "content": "こんにちは"}]}'
```

**→ 完全な導入手順：[docs/快速开始.md](docs/快速开始.md)**（起動 → チャネル設定 → 価格設定 → キー作成 → Claude Code / Codex CLI 接続）。リンク先は現在中国語です。

## デプロイ

- **単一インスタンス**：単一バイナリ + SQLite。外部依存はありません（レート制限 / サーキットブレーカー / セッションアフィニティはプロセス内実装で、機能は低下しません）。
- **Docker / docker-compose**：リポジトリ直下の `docker-compose.yml` を使用します。`./config.yaml:/app/config.yaml:ro` のマウント行をコメント解除してから `docker compose up -d` を実行してください（マウントしない場合、組み込みのデフォルト認証情報は起動ガードで拒否されます）。`TZ` を設定してください（日次コスト上限と利用量集計の日付境界に影響します）。
- **リバースプロキシ**：`Host` / `X-Forwarded-Proto` を転送し（OIDC コールバック URL はシステム設定で明示的に指定）、`/v1/*` のレスポンスバッファリングを無効化し（SSE を溜めない）、`config.yaml` の `server.trusted_proxies` にプロキシのアドレスを設定します（設定しない場合、ゲートウェイは `X-Forwarded-For` を無視し、全ユーザーが 1 つのログインレート制限バケットを共有します）。
- **マルチインスタンス**：MySQL/PG + Redis（レート制限 / サーキットブレーカー / セッションアフィニティを共有。Pub/Sub でルートと価格のホットリロードを配信）。

**→ systemd / Nginx の例、設定リファレンス、バックアップ、セキュリティ強化チェックリストは [docs/部署指南.md](docs/部署指南.md) にあります。** リンク先は現在中国語です。

## プロトコル接続

| 受信エンドポイント | クライアントプロトコル | 送信方法 | 想定ツール |
| --- | --- | --- | --- |
| `POST /v1/chat/completions` | OpenAI Chat | `type=openai` チャネルへ透過 | Cursor、Continue、汎用 OpenAI SDK |
| `POST /v1/messages` | Anthropic Messages | `type=anthropic` チャネルへ透過（`x-api-key` / `Bearer` 対応） | Claude Code、Claude SDK |
| `POST /v1/responses` | OpenAI Responses | `type=openai` チャネルへ透過 | Codex CLI、OpenAI Agents SDK |
| `POST /v1/embeddings` | OpenAI Embeddings | 転送 | 埋め込み呼び出し |
| `POST /v1/images/generations` | OpenAI Images | 転送（呼び出しごとの課金） | 画像生成 |
| `GET /v1/models` | OpenAI Models | ローカルモデル一覧 | — |

> **1 チャネル 1 プロトコル**：チャネルのプロトコルは `Channel.Type` です。上流が両方のプロトコルを提供する場合は、プロトコルごとにチャネルを作成してください。チャネルの「プロトコル変換」を有効にすると、異なるプロトコルのリクエストを chat↔messages 変換でフォールバックできます（変換候補は常に同一プロトコル透過の候補より後になります）。

管理コンソールには **接続ドキュメント** ページがあり、Claude Code / Codex CLI / Cursor などの詳細な設定手順を掲載しています。利用者にそのまま共有できます。

## 技術スタック

- **バックエンド**：Go 1.26 + Gin + GORM（`glebarez/sqlite` 純 Go ドライバ、cgo なし）。JWT 認証。上流認証情報を AES で暗号化
- **フロントエンド**：React 19 + Vite + TypeScript。Tailwind CSS v4 + shadcn/ui（Radix プリミティブ）。TanStack Query。Recharts
- **デザイン**：プライマリブルー `#1E40AF` + アンバーアクセント `#D97706`。ライト / ダークテーマ

## ディレクトリ構成

```
simple-ai-gateway/
├── cmd/server/            # エントリーポイント
├── internal/
│   ├── config/            # 設定読み込み（YAML + デフォルト値）
│   ├── db/ · model/       # DB 接続、GORM モデルとマイグレーション（ID 順に冪等実行）
│   ├── domain/            # ビジネスドメイン
│   │   ├── gateway/       #   ゲートウェイ中核：マルチエンドポイント編成 / ディスパッチ / 課金 / キー選択
│   │   ├── channel/ account/ alias/ apikey/ app/ price/ proxy/
│   │   ├── requestlog/ usage/ keytag/ setting/ cleanup/
│   ├── router/            # インメモリルートテーブル（Pub/Sub でホットリロード）
│   ├── convert/ v1proto/  # chat↔messages 変換、/v1 デュアルプロトコルエラーエンベロープ
│   ├── ratelimit/ breaker/ session/   # レート制限 / サーキットブレーカー / セッションアフィニティ（メモリと Redis）
│   ├── accountpick/ proxypick/ proxydial/  # キープールとプロキシ選択、プロキシ接続
│   ├── audit/             # フォレンジック監査（非同期書き込み / リプレイ / クリーンアップ）
│   ├── storage/           # 監査本文ストレージ抽象化（local / s3）
│   ├── billing/ backup/ ipgeo/ crypto/ redis/
│   ├── middleware/ admin/ portal/     # 認証・レート制限ミドルウェア、管理 API、利用者ポータル API
│   └── server/            # 唯一の依存組み立てポイント：ルート登録 + フロントエンド埋め込み
├── web/                   # React フロントエンド（管理 + ポータル + ランディング）
├── tools/                 # ローカル検証用プロセス（モック上流 / テストプロキシ）
├── scripts/build.sh       # フロントエンド → バックエンドの順序でビルド
└── docs/                  # ドキュメントと設計資料
```

## 設定

設定ファイルは `-c` で指定し、省略時はカレントディレクトリの `config.yaml` を読み込みます。記載のない項目は組み込みデフォルト値を使用します。【必須】の 3 項目をデフォルトのまま、または空にすると、プロセスは**起動を拒否します**:

| 項目 | 説明 | デフォルト |
| --- | --- | --- |
| `server.port` | リッスンポート | `8080` |
| `server.trusted_proxies` | 信頼するリバースプロキシの IP/CIDR。空の場合は TCP ピアのみ信頼 | `[]` |
| `database.driver` / `dsn` | `sqlite` / `mysql` / `postgres` と接続文字列 | `sqlite` / `data/saig.db` |
| `database.max_open_conns` など | コネクションプール設定 | ドライバのデフォルト |
| `redis.enabled` / `addr` | マルチインスタンス用の共有状態（単一インスタンスでは不要） | `false` |
| `admin.username` / `password` | スーパー管理者アカウント（初回起動時のみシード。後で管理画面から変更） | `admin` / **必須** |
| `jwt.secret` / `expire_hours` | 管理コンソール JWT 署名シークレット | **必須** / `24` |
| `security.apikey_prefix` | API キーのプレフィックス | `sk-saig` |
| `security.channel_key_aes_key` | 上流認証情報の AES キー。**16/24/32 バイト必須**。それ以外は起動拒否 | **必須** |
| `log.level` / `format` / `output` | プロセスログ：レベル、text/json、stderr/stdout/ファイル | `info` / `text` / `stderr` |

完全なテンプレートはリポジトリ直下の `config.example.yaml`、主要項目の説明は [docs/部署指南.md](docs/部署指南.md#配置参考)（中国語）を参照してください。

## ドキュメント

| ドキュメント | 内容 |
| --- | --- |
| [docs/快速开始.md](docs/快速开始.md) | 10 分で疎通：起動 → チャネル設定 → 最初のリクエスト → ツール接続 |
| [docs/部署指南.md](docs/部署指南.md) | バイナリ / Docker / リバースプロキシ / マルチインスタンス / バックアップ / セキュリティ強化 |
| [docs/使用手册.md](docs/使用手册.md) | 基本概念、管理画面のページ別解説、利用者ポータル、FAQ |
| [docs/UI设计规范.md](docs/UI设计规范.md) | フロントエンドの配色とコンポーネント規約 |

> これらのドキュメントは現在簡体字中国語のみです。

## 作者とライセンス

- 作者：**ruanun**
- ライセンス：[Apache License 2.0](LICENSE)
- コントリビューション：[CONTRIBUTING.md](CONTRIBUTING.md)（特にコミットメールの規約）を先にお読みください
- セキュリティ報告：公開 issue に認証情報や未修正の脆弱性を投稿しないでください。[SECURITY.md](SECURITY.md) に従って非公開で報告してください
