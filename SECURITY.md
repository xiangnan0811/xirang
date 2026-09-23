# 安全政策

[文档入口](docs/README.md) · [运维指南](docs/admin/README.md)

## 报告漏洞

如果你发现了安全漏洞，**请不要在公开 Issue 中披露**。

请通过以下方式私下联系我们：

- 使用 [GitHub 私密漏洞报告](https://github.com/xiangnan0811/xirang/security/advisories/new) 提交安全报告

请附上受影响版本、复现步骤、影响范围及已脱敏的证据，不提交生产密钥、密码或用户数据。

我们会在收到报告后尽快确认并着手修复。

## 支持的版本

| 版本 | 支持状态 |
|------|---------|
| 最新发布版 | 安全更新支持 |
| 开发版 (`main`) | 积极维护 |

## 安全建议

部署 Xirang 时，请确保：

- `JWT_SECRET`、`DATA_ENCRYPTION_KEY`、`METRICS_TOKEN` 使用强随机值；首次创建管理员时，为 `ADMIN_INITIAL_PASSWORD` 设置强密码
- 启用 HTTPS 并使用有效的 TLS 证书
- 生产环境设置 `APP_ENV=production`
- 开启 `INTEGRATION_BLOCK_PRIVATE_ENDPOINTS=true`，阻止通知集成访问私网或回环目标；该开关不是所有出站请求的通用 SSRF 防护
- 开启 `SSH_STRICT_HOST_KEY_CHECKING=true`，维护可信 `known_hosts` 并核对远端主机指纹
- 定期更新到最新版本

密钥强度、默认值、覆盖顺序和首次启动条件见[环境变量](docs/env-vars.md)，部署与 TLS 配置见[部署指南](docs/deployment.md)。源码运行可参考 [backend/.env.production.example](backend/.env.production.example)，容器部署使用 [.env.deploy](.env.deploy)。
