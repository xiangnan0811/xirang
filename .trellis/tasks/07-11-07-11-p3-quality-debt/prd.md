# PRD: P3 质量债

## 范围与状态矩阵

| 项目 | 本分支状态 | 说明 |
|---|---|---|
| CAPTCHA 生成端点限流 | 已完成 | 复用 login limiter，避免 challenge store 滥用 |
| Service Worker `controllerchange` | 已完成 | 新 SW 接管后只 reload 一次 |
| automation i18n `any` | 已完成 | 改用 typed dotted keys + defaultValue |
| Nginx CSP | 已完成 | `connect-src` 默认收紧，可显式追加 WS origin |
| bootstrap `log.Printf` | 已完成本次触及路径 | 改为结构化 module logger，不记录 secret |
| Middleware envelope 全量统一 | 延期 | 需单独枚举与回归全部 middleware |
| panel-editor RAF ref | 延期 | 当前工作树未触及 |
| 其余历史 `log.Printf` | 延期 | 不扩大本次改动面 |
| B-P2-8 / WebSocket 鉴权评估与改造 | 延期 | 父任务明确不重写协议 |

## 本轮验收

- 已完成项必须随本分支完整门禁通过并写入父任务集成记录。
- 延期项保持明确，不得因部分落地而把本任务标记 completed/archive。
- 后续应按独立、可回滚的小任务实现，并分别补测试与 spec。

## 状态

保持 `planning` / active。当前分支只提交已完成项，不归档本任务。
