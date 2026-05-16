## 概述

简要说明本 PR 的改动内容和目的。

## 改动类型

- [ ] 新功能 (feat)
- [ ] Bug 修复 (fix)
- [ ] 文档 (docs)
- [ ] 重构 (refactor)
- [ ] 测试 (test)
- [ ] 其他

## 自测清单

- [ ] PR 标题符合 Conventional Commits（例如 `feat(web): ...`）
- [ ] 后端：`cd backend && go test ./...` 通过
- [ ] 前端：`cd web && npm run check` 通过
- [ ] 文档/流程：`bash scripts/check-doc-freshness.sh` 通过或提醒已处理
- [ ] 迁移安全：涉及 migration 时 `bash scripts/check-migration-utc-safety.sh` 通过
- [ ] 已在本地验证功能正常
- [ ] 无安全风险（无硬编码密钥、无 SQL 注入等）
- [ ] 涉及接口/模型/页面/配置变更时已同步更新文档
- [ ] 涉及 release / image / deploy / version-check 变更时，已同步更新 `README.md`、`docs/deployment.md`、`docs/env-vars.md`、`docs/maintainers/release.md`

## 合并与发布监控

- [ ] PR 创建后会持续监控 required CI jobs；失败时在本分支修复、推送并重新监控
- [ ] Maintainer 仅在 required checks 全部通过后合并
- [ ] 合并后会检查 `Release Please` / auto release / `Publish Docker Images`，以及适用的 `Sync Docker Hub Description` post-merge automation
- [ ] 若本次合并不触发正式 release，会在交付记录中说明未预期生成 GitHub Release 或 Docker Hub 镜像发布
