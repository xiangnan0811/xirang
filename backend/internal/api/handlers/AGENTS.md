# API handlers 局部入口

遵循 [项目入口](../../../../AGENTS.md)。本目录保存按资源组织的薄 REST handler，
局部测试与实现同目录，路由注册及 RBAC 集成测试位于上一层 `api/`。

按修改内容加载 [后端目录](../../../../docs/spec/backend/directory-structure.md)、
[错误与响应](../../../../docs/spec/backend/error-handling.md)、
[质量约定](../../../../docs/spec/backend/quality-guidelines.md)。认证、权限、脱敏及领域
错误的具体要求由 [领域合同](../../../../docs/spec/domains/README.md) 维护。
不要把本入口当作独立接口或权限清单。
