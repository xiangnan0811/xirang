# 安全加固

本文档汇总生产部署 Xirang 时应关注的安全配置。漏洞报告方式见 [安全政策](../../SECURITY.md)。

## 必填密钥

生产环境必须设置强随机值：

| 变量 | 用途 | 建议 |
|---|---|---|
| `ADMIN_INITIAL_PASSWORD` | 首次启动创建 `admin` 用户 | 使用一次性强密码，登录后尽快修改。 |
| `JWT_SECRET` | JWT 签名密钥 | 至少 16 字符，建议使用密码管理器生成。 |
| `DATA_ENCRYPTION_KEY` | 敏感字段加密密钥 | 使用强随机值，建议 32 字节 base64。 |

`APP_ENV=production` 时弱密钥或缺失必填项会导致服务拒绝启动。

## HTTPS

All-in-One 容器只提供 HTTP 单入口 `10761`。公网 HTTPS 应由外部反向代理或负载均衡终止 TLS，例如 Caddy、Nginx Proxy Manager、Nginx 或云厂商网关。

反向代理建议：

- 只将外部代理暴露到公网，按需限制宿主机 `10761` 端口的访问来源。
- 保留 `Host`、`X-Forwarded-For`、`X-Forwarded-Proto` 请求头。
- 支持 WebSocket Upgrade，确保实时日志和终端连接可用。
- 使用外部域名访问时，在 `.env` 中配置 `CORS_ALLOWED_ORIGINS=https://xirang.example.com`。

## SSH 主机校验

生产环境建议保持：

```env
SSH_STRICT_HOST_KEY_CHECKING=true
SSH_AUTO_ACCEPT_NEW_HOSTS=true
SSH_KNOWN_HOSTS_PATH=/data/.ssh/known_hosts
```

含义：

- 首次连接新主机时自动接受并持久化指纹。
- 已知主机指纹变化时拒绝连接，避免中间人攻击。
- All-in-One 镜像默认把 known_hosts 放在 `/data` 下，随数据卷持久化。

如需禁用首次自动接受，设置：

```env
SSH_AUTO_ACCEPT_NEW_HOSTS=false
```

## Webhook / 通知 SSRF 防护

生产环境建议保持：

```env
INTEGRATION_BLOCK_PRIVATE_ENDPOINTS=true
```

该配置会阻断 Webhook、Slack、Telegram 等通知端点指向私网或回环地址，降低 SSRF 风险。

## 登录防护

相关配置：

| 变量 | 默认值 | 说明 |
|---|---|---|
| `LOGIN_RATE_LIMIT` | `10` | 登录接口速率限制次数。 |
| `LOGIN_RATE_WINDOW` | `1m` | 登录速率限制窗口。 |
| `LOGIN_FAIL_LOCK_THRESHOLD` | `5` | 连续失败多少次后锁定账号。 |
| `LOGIN_FAIL_LOCK_DURATION` | `15m` | 锁定持续时间。 |
| `LOGIN_CAPTCHA_ENABLED` | `false` | 登录验证码默认值，可在系统设置中调整。 |
| `LOGIN_SECOND_CAPTCHA_ENABLED` | `false` | 二次验证码默认值，可在系统设置中调整。 |

用户可在个人设置中启用 TOTP 两步验证，兼容 Google Authenticator 等应用。

## 敏感字段保护

Xirang 会加密存储 SSH 密码、SSH 私钥、TOTP 密钥、通知端点、代理地址等敏感字段。请妥善备份 `DATA_ENCRYPTION_KEY`；数据库备份没有对应密钥时无法恢复敏感字段明文。
