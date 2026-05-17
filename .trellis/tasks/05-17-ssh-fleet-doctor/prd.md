# SSH Fleet Doctor MVP

## Goal

为节点连接和备份运行环境提供安全的只读诊断能力，让用户知道 SSH、sudo、工具、目录、磁盘和探针问题在哪里以及下一步该怎么处理。

## Requirements

* 提供 allowlisted diagnostics，不支持用户输入任意 shell 命令。
* 诊断范围包括 SSH 连接、认证方式、known_hosts、sudo、备份目录读写、磁盘空间、基础工具和 probe 状态。
* 输出结构化结果：check、status、evidence、suggestion。
* 前端在节点上下文中展示 Doctor 结果。
* 第一轮只诊断不自动修复。

## Acceptance Criteria

* [ ] 用户能从节点页面运行或查看 SSH Fleet Doctor。
* [ ] 诊断结果能区分连接失败、认证失败、known_hosts 冲突、sudo 不可用、工具缺失、目录不可写、磁盘不足等常见问题。
* [ ] 后端不接受任意命令字符串作为诊断输入。
* [ ] API 响应不泄露密码、私钥、代理地址等敏感信息。
* [ ] 后端测试覆盖 allowlist、安全输入和典型诊断结果。

## Definition of Done

* Backend tests added/updated for diagnostic checks and API behavior.
* Frontend tests/checks added or updated for Doctor UI.
* Code-spec updated for diagnostic check/result contracts.
* `go -C backend test ./...`, `npm --prefix web run check`, and `git diff --check` pass before completion.

## Out of Scope

* Arbitrary remote command execution.
* Auto-fix or remediation.
* Bulk fleet remediation.
* Long-running background diagnostics unless required by implementation constraints.

## Technical Notes

* Existing foundations include Node/SSHKey models, `backend/internal/sshutil/`, `backend/internal/probe/`, task executors, sudo helper, node handlers/tests, batch command flow, and node detail UI.
