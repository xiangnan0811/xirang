# 安全政策

## 报告漏洞

如果你发现了安全漏洞，**请不要在公开 Issue 中披露**。

请通过以下方式私下联系我们：

- 使用 [GitHub Security Advisories](../../security/advisories/new) 提交安全报告

我们会在收到报告后尽快确认并着手修复。

## 支持的版本

| 版本 | 支持状态 |
|------|---------|
| 最新发布版 | 安全更新支持 |
| 开发版 (master) | 积极维护 |

## 安全建议

部署 Xirang 时，请确保：

- 所有密钥（`JWT_SECRET`、`DATA_ENCRYPTION_KEY`、`ADMIN_INITIAL_PASSWORD`）使用强随机值
- 启用 HTTPS 并使用有效的 TLS 证书
- 生产环境设置 `APP_ENV=production`
- 开启 `INTEGRATION_BLOCK_PRIVATE_ENDPOINTS=true` 防止 SSRF
- 开启 `SSH_STRICT_HOST_KEY_CHECKING=true` 防止中间人攻击
- 定期更新到最新版本

详见 `backend/.env.production.example` 中的安全相关配置。
