# 安全策略

simple-ai-gateway 是自托管的 AI 网关，会接触上游 API Key、业务令牌与请求正文。请按以下方式报告安全问题。

## 报告漏洞

**不要开公开 issue。** 请使用 GitHub 的私密漏洞报告：

1. 打开仓库的 **Security** 标签页；
2. 选择 **Report a vulnerability**（GitHub Security Advisories）；
3. 说明影响版本、复现步骤、影响面与可能的修复方向。

如果无法使用该功能，请通过仓库 owner 的 GitHub 主页所列联系方式私下联系。

## 支持范围

- 仅处理**最新 Release**（尚未发布时以 `master` 为准）及 `master` 分支上的问题；
- 已停止维护的旧版本不再单独发补丁；
- 部署侧配置（反向代理、TLS、网络隔离、S3/Redis 凭证）不在本项目代码范围内，但欢迎报告文档缺陷。

## 披露约定

- 修复发布后再公开细节；
- 报告者可在 Release Notes 中署名（如愿意）；
- 请勿在报告中附带真实生产凭证；复现请使用测试 key。
