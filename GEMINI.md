# Xirang (息壤) - Gemini 上下文

> 技术栈、项目结构、开发命令等完整信息请参阅 [CLAUDE.md](CLAUDE.md)，本文件仅补充 Gemini CLI 差异。

## 项目简介

Xirang 是一个轻量的服务器运维管理平台，通过 SSH 管理目标服务器，支持多引擎备份（Rsync/Restic/Rclone）、任务编排、多渠道告警、Web 终端、SLA 报告等功能。

- 后端：Go 1.24 + Gin + GORM + SQLite/PostgreSQL
- 前端：React 18 + TypeScript + Vite + Tailwind CSS + Radix UI

## 快速校验

```bash
cd backend && go test ./...          # 后端测试
cd web && npm run check              # 前端 typecheck + test + build
```

## 代码约定

- 默认简体中文沟通与注释
- 提交格式：`feat(web): ...` / `fix(backend): ...`
- 前端优先复用 `components/ui/` 已有组件
- 任何"已完成"结论必须基于实际命令输出
