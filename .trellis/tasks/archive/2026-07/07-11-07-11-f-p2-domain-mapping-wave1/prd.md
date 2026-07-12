# PRD: Domain 映射 Wave1（F-P2-2）

## 问题

credentials、app credentials、node metrics 与 silences 的 wire DTO 被组件直接消费，snake_case、空数组、非法数值和日期在 React state 中扩散；auth CAPTCHA response 也缺少稳定的 boundary mapper。

## 实际落地

- 各 API module 内定义 raw response/request type 与 `map*`，组件只消费 camelCase domain model。
- credentials / app credentials 映射 ID、时间、验证状态、配置字段，并保持 secret 输入与记录响应分离。
- node metrics 映射 snapshot/history/latest/status/forecast，并对数组与有限数值做防御性归一化。
- silences 映射 `match_node_id`、`match_category`、`match_tags`、时间窗及创建者字段；请求在 API boundary 转回 snake_case。
- auth CAPTCHA 映射 primary/second challenge 可选字段，登录请求仅提交当前启用通道。
- 更新 mock、组件、页面和域类型，不在 UI 中保留 raw snake_case 访问。

## 验收矩阵

| 输入 | 预期 |
|---|---|
| 缺失/非数组集合 | 映射为 `[]` |
| 非有限数值 | 映射为安全 finite fallback，不产生 `NaN` |
| 可选时间/字段缺失 | 映射为稳定的 `undefined` / 空值语义 |
| unknown status/type | 降级为非授权/非成功的安全值 |
| create/update request | 只在 API boundary 序列化 snake_case |

## 验证

- 新增 `credentials.test.ts`、`node-metrics-api.test.ts`、`silences.test.ts`、`auth-api.test.ts`，并更新消费端测试。
- `env -u NODE_ENV npm run check` 已通过（127 files / 547 tests）；Trellis/spec 更新后须重跑。

## 状态

实现完成，位于 `fix/07-11-audit-p1-security`；工作提交后归档。
