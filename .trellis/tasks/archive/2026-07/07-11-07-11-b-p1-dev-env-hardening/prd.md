# PRD: P1 开发环境密钥 hardening

## 问题与风险

1. `IsDevelopmentEnv()` 将 `GIN_MODE=debug` 视为开发环境，导致 `APP_ENV=production` 时仍可用默认 JWT / 跳过强密钥校验。
2. 开发路径生成临时 DEK 时把 `generated_key` 打进日志。
3. 生产配置对 JWT、DEK、metrics token、代理来源及已有 admin 场景的约束不一致。

## 实际落地

- `IsDevelopmentEnv` 只识别显式 `APP_ENV` / `ENVIRONMENT=development`；Gin debug 不再降低安全级别。
- 生产 JWT 至少 32 字符；DEK 至少 16 字符；metrics token 至少 16 字符并拒绝文档占位符。
- `TRUSTED_PROXIES` 在配置层解析/校验，Gin 默认 trust-none，非法值启动失败。
- `ADMIN_INITIAL_PASSWORD` 只在数据库尚无 admin 时强制；已有 admin 的升级实例可留空。
- 开发态临时 DEK 仍可生成，但日志不再包含密钥明文。
- env examples 与部署文档同步实际启动契约。

## 验收矩阵

| 场景 | 预期 |
|---|---|
| production + Gin debug + 弱 JWT | 拒绝启动 |
| development + 空 JWT | 保留本地兼容默认值 |
| production + 空/弱 metrics token | 拒绝启动 |
| 无已有 admin + 空初始密码 | bootstrap 失败 |
| 已有 admin + 空初始密码 | 正常启动，不重置账号 |
| 非法 trusted proxy | 配置加载失败 |
| 临时 DEK | 日志只记录事件，不记录 key |

## 验证

- config、bootstrap、util env、router trusted proxy 与日志相关测试覆盖上述矩阵。
- 后端完整测试、lint、build 与文档新鲜度检查已通过；Trellis/spec 更新后须重跑。

## 状态

实现完成，位于 `fix/07-11-audit-p1-security`；工作提交后归档。
