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

All-in-One 镜像检测到以下证书文件时自动启用 HTTPS：

- `/etc/nginx/certs/fullchain.pem`
- `/etc/nginx/certs/privkey.pem`

Docker Compose 部署时把证书放到 `./certs`，并在 `docker-compose.prod.yml` 中启用：

```yaml
- ./certs:/etc/nginx/certs:ro
```

未挂载证书时容器使用 HTTP 模式，适合内网测试或由外部反向代理终止 TLS 的场景。

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

以下敏感字段通过 GORM hooks 自动加密或脱敏，不应在 handler、日志或审计详情中直接输出明文：

- 节点 SSH 密码与私钥。
- SSH Key 私钥。
- 用户 TOTP secret 与恢复码。
- 通知渠道 endpoint、secret、proxy URL。
- 任务中需要保护的命令/路径相关字段（以模型 hook 实现为准）。

密钥轮替时可临时设置 `DATA_ENCRYPTION_LEGACY_KEY` 用于解密旧数据，确认轮替完成后应移除。

## 指标端点保护

`/metrics` 会暴露路由标签和流量画像。生产环境建议配置：

```env
METRICS_TOKEN=<强随机 token>
METRICS_RATE_LIMIT=5
METRICS_RATE_WINDOW=1s
```

Prometheus 抓取侧使用 Bearer token：

```yaml
bearer_token_file: /etc/prometheus/secrets/xirang-metrics-token
```

## 审计与权限

- 使用 RBAC 控制用户可访问的节点、策略、任务和系统功能。
- 重要操作会写入审计日志。
- Web 终端和 WebSocket 相关路径使用独立认证/审计逻辑。
- 为不同人员创建独立账号，不要共享 admin 密码。

## 生产检查清单

- [ ] `APP_ENV=production`
- [ ] `ADMIN_INITIAL_PASSWORD`、`JWT_SECRET`、`DATA_ENCRYPTION_KEY` 已设置强随机值
- [ ] 已启用 HTTPS 或确认由外部反向代理终止 TLS
- [ ] `INTEGRATION_BLOCK_PRIVATE_ENDPOINTS=true`
- [ ] `SSH_STRICT_HOST_KEY_CHECKING=true`
- [ ] `METRICS_TOKEN` 已配置或确认 `/metrics` 不暴露到公网
- [ ] 已备份 `.env`、`./data` 和 `./backups`
- [ ] 管理员账号启用 TOTP
