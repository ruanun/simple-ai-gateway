# 贡献指南

## Git 身份：统一使用项目公开邮箱

本项目提交邮箱统一使用 **ruanun@foxmail.com**（已确认可公开），不要使用其他私人邮箱：

```bash
git config user.name "ruanun"
git config user.email "ruanun@foxmail.com"
```

外部贡献者如不希望公开自己的邮箱，可以使用 GitHub noreply 地址；仓库维护者的提交统一为上面的地址。

## 提交前检查

```bash
# 前端
cd web && pnpm install --frozen-lockfile && pnpm lint && pnpm test && pnpm build && cd ..

# 后端
go vet ./...
go test ./...

# 密钥扫描（需要 gitleaks）
gitleaks git --config .gitleaks.toml --redact -v .
```

不要在提交中放入真实凭证、生产数据库、审计正文或 `reference/` 下的第三方源码。
