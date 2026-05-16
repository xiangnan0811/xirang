# 开源文档全面更新

## Goal

让 Xirang 的公开文档准确反映当前项目状态，并符合开源项目的阅读路径：README.md 作为精简入口，docs/ 作为详细用户/管理员/维护者指南，删除或迁出过期、归档、过程类文档，避免远程仓库继续承载开发过程材料。

## What I already know

* 用户希望全面更新 README.md 和 docs/，不是局部修补。
* README.md 应保持简洁：保留产品定位、核心亮点/功能概览、简单部署步骤和文档链接，不承载完整手册。
* 当前 README.md 同时包含亮点、详细功能、部署、更新、备份、FAQ，内容偏重且与 docs/deployment.md 重复。
* docs/ 当前混合了用户功能文档、部署/环境变量文档、维护者发布手册、历史 specs/plan/design 文档、UI gallery 静态样例。
* docs/specs/*.md 是明显的开发过程/规格/计划文档，不应作为公开用户文档保留。
* docs/migration-utc-cutover.md 是一次性迁移 runbook，针对 000050；当前迁移已到 000057_service_uptime。
* 后端 Go 版本以 backend/go.mod 为准，当前为 Go 1.26.3；部分 agent/doc 文档仍写 Go 1.24，已过期。
* 当前最新迁移以 backend/internal/database/migrations 为准，SQLite 最新为 000057_service_uptime；CLAUDE.md 仍写 000047，已过期。
* 官方镜像与部署配置以 docker-compose.prod.yml、.env.deploy、deploy/allinone/* 为准：镜像 `docker.io/linnea7171/xirang`，All-in-One 单容器，HTTP 8080 / HTTPS 8443，数据 ./data，备份 ./backups。
* 文档规范已写入 memory：开源文档应 lean/current/user-facing，过程/归档/开发过程材料不能推送到远程。

## Research References

* [`research/open-source-docs-conventions.md`](research/open-source-docs-conventions.md) — 成熟自托管开源项目通常把 README 作为入口，把部署/管理/开发细节放入结构化 docs，并把过程/贡献者材料与用户手册分离。

## Requirements

* README.md 改为开源项目入口页：
  * 简短产品定位和适用场景。
  * 将“亮点”和“功能”整合成一个高层概览，避免逐功能长篇展开。
  * 保留最小 Docker Compose 部署路径和源码开发启动入口。
  * 将详细部署、环境变量、备份恢复、升级、功能使用等内容链接到 docs/。
* docs/ 重新组织为更清晰的信息架构，优先面向用户/管理员：
  * 部署与升级。
  * 配置与环境变量。
  * 备份/恢复/快照/保留策略。
  * 监控/告警/状态页/异常检测。
  * 自动化与任务编排。
  * 维护者或贡献者文档（如仍保留）应单独分区并明确不是用户手册。
* 删除或迁出公开仓库中的过程/归档/过期文档：
  * `docs/specs/*.md` 过程设计/计划文档。
  * `docs/migration-utc-cutover.md` 一次性历史迁移 runbook，除非提炼后并入升级/迁移注意事项。
  * `docs/ui-gallery/*` 静态 UI 样例，除非作为开发者设计参考被明确保留在 maintainer/developer 区。
* 提炼有用内容后再删除原文档，避免丢失仍对用户有价值的部署、恢复、监控、备份、通知、安全信息。
* 所有文档陈述必须通过当前代码、配置、脚本、迁移或已有文档交叉验证；不能凭空补充不存在的功能、命令或路线图。
* 更新文档中的过期事实：Go 版本、迁移版本、分支名、部署端口、镜像名、环境变量、当前功能状态。
* 避免把 Trellis/开发过程/规划文档纳入公开 docs 导航或 README 链接。
* 直接删除 tracked 的 `docs/specs/` 和 `docs/ui-gallery/`，不在仓库内移动到 archive/developer 区；如需参考只能本地或私有保留。
* 删除 `docs/specs/` 不会影响 Trellis 运行，因为 Trellis 当前读取的是 `.trellis/spec/`、`.trellis/tasks/`、`.trellis/workflow.md` 等路径；但需要同步更新 `.trellis/spec/guides/documentation-truth-guide.md` 与相关索引，避免后续会话继续把 `docs/specs/` 当作历史文档处理。

## Acceptance Criteria

* [ ] README.md 不再重复完整部署/备份/升级手册，阅读路径清晰，功能与亮点合并为一个简洁概览。
* [ ] docs/ 顶层有清晰索引或 README，能引导用户找到部署、配置、使用、运维、维护者内容。
* [ ] 过期/过程/归档文档从远程可见的 tracked docs 中删除，或只保留经确认的维护者/开发者参考并明确定位。
* [ ] 被保留或新建的 docs 内容能从当前代码/配置/脚本验证，不包含未实现能力。
* [ ] 部署文档足够详细，用户可按文档完成 Docker Compose 部署、HTTPS 配置、升级/回滚、备份/恢复和常见排障。
* [ ] 环境变量文档与 .env.deploy、backend/.env.production.example、backend/.env.example、web/.env.example 保持一致。
* [ ] 文档中不再出现明显过期的 Go 1.24、migration 000047、master 分支等当前状态错误。
* [ ] 删除清单和迁移后的信息归位可通过 git diff 审核。
* [ ] `.trellis/spec/guides/documentation-truth-guide.md` 不再要求保留 `docs/specs/` 历史文档，而是明确公开仓库不保留过程/归档文档。

## Definition of Done

* 文档重组和改写完成。
* 运行适合文档变更的校验：至少执行 docs freshness/script 检查（如适用）、链接/路径抽查、必要的 grep 校验过期关键词。
* 不提交生成的大型过程文档或 archive 文档到远程。
* 变更保持最小可审查：只改文档和必要的文档索引/配置，不改业务代码。

## Out of Scope

* 不实现新功能。
* 不重写 UI 或 API 行为。
* 不修改发布流水线，除非发现文档校验脚本必须同步维护。
* 不手动改写 generated Swagger 文件。
* 不保留新的 Trellis/PRD/过程文档到公开 docs/。

## Technical Approach

1. 以 README.md 作为入口页重写，减少重复内容。
2. 建立 docs/ 信息架构，并将现有有价值内容合并到主题文档中。
3. 对过程/历史/一次性文档先抽取仍有价值的信息，再从 tracked docs 删除原文件。
4. 使用当前仓库文件作为事实源：compose/env/Dockerfile/scripts/migrations/go.mod/package.json/API docs。
5. 最后用 grep 和脚本检查过期关键词、断链和文档新旧状态。

## Decision (ADR-lite)

**Context**: 当前文档把用户手册、维护者流程、过程 specs、一次性 runbook、静态 UI demo 混在 docs/，README 又承载了过多细节，开源后会降低可信度和可读性。

**Decision**: README 作为轻量入口；docs/ 按用户任务组织；过程/归档/历史计划文档不作为公开文档保留；维护者内容如需保留必须单独分区并清晰标注。

**Consequences**: 删除文件会让 git diff 较大，但能明显瘦身远程仓库；若少数历史 runbook 对维护者仍有价值，需要在本地或私有位置保留，而不是公开 docs/。

## Open Questions

* 暂无。

## Technical Notes

* Inspected: README.md, docs/deployment.md, docs/env-vars.md, docker-compose.prod.yml, .env.deploy, backend/go.mod, backend/internal/database/migrations.
* Inventory by Explore agent identified high-value disposition candidates for README, deployment docs, env vars, release maintainer docs, backend README, CLAUDE/GEMINI, docs/specs, migration cutover runbook, and ui-gallery.
* External research persisted in research/open-source-docs-conventions.md.
