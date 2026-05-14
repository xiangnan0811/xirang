# 修复 admin 访问服务监控 403

## Goal

修复服务监控页面在 admin 登录状态下请求 `/api/v1/service-monitors` 返回 403 Forbidden 的问题，确保管理员可以正常进入服务监控页面并加载监控列表。

## What I already know

* 用户使用 admin 登录进入服务监控页面。
* 浏览器 console 报错：`GET http://192.168.100.247:19927/api/v1/service-monitors 403 (Forbidden)`。
* 目标是定位“权限不足”的原因并修复。

## Assumptions (temporary)

* admin 账号应拥有访问服务监控列表的权限。
* 403 更可能来自后端权限/RBAC/ownership 中间件，而不是认证失效；若认证失效通常会是 401。

## Requirements

* admin 登录后访问服务监控页面不应因为权限配置返回 403。
* `admin` 对服务监控拥有读写权限。
* `operator` 对服务监控拥有读写权限。
* `viewer` 对服务监控拥有只读权限。
* 修复应尽量最小化影响面，不改变其他资源的既有权限边界。

## Acceptance Criteria

* [x] `admin` 拥有 `service_monitors:read` 与 `service_monitors:write`。
* [x] `operator` 拥有 `service_monitors:read` 与 `service_monitors:write`。
* [x] `viewer` 拥有 `service_monitors:read`，但不拥有 `service_monitors:write`。
* [x] 相关权限测试覆盖服务监控权限映射。
* [x] 后端测试通过。

## Definition of Done (team quality bar)

* Tests added/updated where appropriate.
* Backend `go test ./...` passes if backend changed.
* Frontend checks only在前端改动时运行。
* 行为变化的风险和回滚方式已考虑。

## Out of Scope (explicit)

* 不重做服务监控页面交互。
* 不放宽普通用户权限，除非代码确认这是既有产品预期。
* 不引入新的权限模型。

## Technical Notes

* 待检查：服务监控 API 路由、RBAC 权限映射、admin 角色初始化/权限判断、前端 API 调用。
