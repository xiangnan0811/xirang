# PRD: 节点详情 i18n（F-P2-1）

## 问题与扩展范围

`web/src/features/nodes-detail/**` 大量硬编码中文，en locale 破损。
二次审查还发现首次/失败探测会被误绘制为 offline、页面与 Overview tab 重复轮询、切换 node 时局部状态可能残留，以及部分 tab 缺少显式错误态。

## 实际落地

- node-detail overview/metrics/tasks/alerts/profile/forecast/trend 的可见字符串全部走 `t()`，中英文 key 同步。
- 顶层只保留一份 status poll，并把状态/错误下发给 Overview tab，消除重复 `/status` 请求。
- 只有存在有效 `probedAt` 时才把 `online=false` 显示为 offline；首次/无样本/失败为 unknown 或明确错误。
- node ID 变化时给各 tab 使用稳定 remount key，清除前一节点的局部 state。
- metrics、tasks、alerts、profile、forecast 等路径区分 loading/empty/error，并保持可访问 `role=alert`。
- domain 类型统一为 camelCase status/metric fields。

## 验收

- 英文 locale 下不出现本次触及的裸中文。
- 首次加载、无探测样本、离线、请求失败四种状态不会互相冒充。
- 页面 header 与 Overview tab 只共享一次 status polling。
- 切换 node 后不展示前一节点数据；各 tab 错误可见。
- node-detail 相关 Vitest 与完整 `npm run check` 通过。

## 验证

- 更新 page/tab/chart/forecast 测试，并新增 `use-node-status.test.ts`。
- `env -u NODE_ENV npm run check` 已通过（127 files / 547 tests）；Trellis/spec 更新后须重跑。

## 状态

实现完成，位于 `fix/07-11-audit-p1-security`；工作提交后归档。
