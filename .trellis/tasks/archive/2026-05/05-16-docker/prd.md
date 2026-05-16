# 简化 Docker 部署配置

## Goal

清理 Xirang 当前过度变量化、双端口、内置 HTTPS 分支的 Docker 部署路径，收敛为官方单容器镜像、单应用端口、固定日志目录和外部反向代理负责 HTTPS 的部署模型，避免还没有用户前就背负无意义兼容包袱。

## Requirements

- 删除旧开发用途的根目录 `docker-compose.yml` 内容，不再维护开发用 Compose 部署路径；简化后的生产 Compose 使用根目录 `docker-compose.yml`。
- 修改生产 Compose：
  - 镜像名固定为 `linnea7171/xirang`，不通过 registry/namespace/image 变量拼接。
  - 不再暴露 `HTTP_PORT` / `HTTPS_PORT` 两个端口变量。
  - 只映射一个宿主机端口到容器内固定端口 `10761`。
  - 保留 `IMAGE_TAG` 作为版本选择方式，默认 `latest`。
  - 保留 `.env` 注入、安全必填项、数据目录和备份目录挂载。
  - 增加固定日志目录挂载；容器内日志路径固定，像 `/data` 一样可映射到宿主机。
  - 删除 Compose `logging` 配置。
- 修改 All-in-One 镜像/Nginx 配置：
  - Nginx 只监听容器内 `10761`。
  - 移除证书检测、HTTP/HTTPS 双模板和 `8080/8443` 对外入口。
  - 健康检查使用 `http://127.0.0.1:10761/healthz`。
  - 后端仍可作为内部进程监听本地端口，Nginx 继续代理 `/api/v1/*`、`/healthz` 和前端 SPA。
  - 应用日志写到固定容器内路径，并确保目录可被挂载和非 root 用户写入。
- 同步清理用户文档、维护者文档、README、环境变量示例、Makefile/deploy-kit 生成说明中旧部署路径：
  - 不再说明内置 HTTPS、证书挂载、`HTTP_PORT`、`HTTPS_PORT`、`docker-compose.yml` 开发部署或可变镜像命名。
  - 明确 HTTPS/TLS 应由用户自行通过外部 Caddy、Nginx Proxy Manager、Nginx 等反向代理处理。
  - 文档中的日志排障应指向固定宿主机日志目录和 `docker compose logs` 的当前行为。
  - Compose 文件名统一为根目录 `docker-compose.yml`，不再保留旧生产 Compose 文件。

## Acceptance Criteria

- [ ] 根目录不再包含开发用途的 Compose 配置。
- [ ] `docker-compose.yml` 使用固定 `linnea7171/xirang:${IMAGE_TAG:-latest}`，只暴露一个 `10761` 容器端口映射，并且没有 `HTTP_PORT` / `HTTPS_PORT` / `logging:`。
- [ ] 根目录不再包含旧生产 Compose 文件。
- [ ] All-in-One 镜像只 `EXPOSE 10761`，健康检查访问 `127.0.0.1:10761/healthz`。
- [ ] Nginx 模板只监听 `10761`，没有 TLS 证书配置、HTTP 到 HTTPS 重定向或 `8443`。
- [ ] Entrypoint 不再根据证书切换 Nginx 模板。
- [ ] 部署文档、README、环境变量说明不再引用已删除端口变量、内置 HTTPS 证书挂载或开发 Compose 文件。
- [ ] 日志目录固定并支持宿主机挂载，相关文档说明清楚。
- [ ] 运行 `docker compose -f docker-compose.yml config` 通过。
- [ ] 运行必要的代码/文档质量检查；如跳过重量级镜像构建，需记录原因。

## Definition of Done

- 修改保持最小影响面，不引入新依赖。
- 文档声明必须和当前 Compose/Dockerfile/Nginx 实现一致。
- 不保留旧部署路径兼容说明，因为当前开源但尚无用户。
- 与部署行为相关的 env 示例和文档同步更新。
- 完成 Trellis check 或等价质量检查后再汇报完成。

## Technical Approach

采用直接收敛方案：删除旧开发 Compose；生产 Compose 固定官方镜像和单端口；All-in-One 镜像保留“后端内部端口 + Nginx 前端入口”的结构，但将 Nginx 对外入口统一为 `10761`，删除 TLS 自动检测分支和 HTTP-only 备用模板；把后端 `LOG_FILE` 默认写到固定 `/logs/xirang.log`，并在镜像和 Compose 中准备 `/logs` 挂载。文档同步改为“项目只提供 HTTP 单入口，HTTPS 在外部反向代理层完成”。

## Decision (ADR-lite)

**Context**: 当前部署配置包含开发 Compose、生产 Compose 变量化镜像名、HTTP/HTTPS 双端口、证书自动检测和 Compose logging，超过项目当前实际支持边界，且会误导自部署用户认为项目负责 TLS 编排。

**Decision**: 直接删除和改写旧路径，不做兼容 shim；官方部署只支持 `linnea7171/xirang` 单容器镜像、容器内 `10761` 单入口、固定日志目录和外部代理 TLS。

**Consequences**: 配置更简单、文档更真实；旧的 `80/443` 与内置证书挂载说明会消失。因为项目尚无用户，不承担旧配置迁移成本。

## Out of Scope

- 不新增新的部署方式（Kubernetes、systemd、Helm 等）。
- 不实现项目内 HTTPS 证书管理或自动申请证书。
- 不重构后端日志系统；只设置容器部署默认日志文件路径和目录。
- 不修改 Docker 镜像发布 workflow，除非检查发现它硬编码了已删除端口/文件。
- 不改变源码本地运行端口：后端开发仍可使用 `:8080`，Vite 开发仍可使用 `:5173`。

## Technical Notes

- 旧生产 Compose 使用 `IMAGE_REGISTRY` / `IMAGE_NAMESPACE` / `IMAGE_TAG` 拼接镜像，映射 `HTTP_PORT` 和 `HTTPS_PORT`，并配置 Compose `logging`。
- 旧 `docker-compose.yml` 是开发用双容器 Compose，用户明确要求它完全没有存在必要；简化后的生产 Compose 最终应使用 `docker-compose.yml` 文件名。
- 当前 `deploy/allinone/Dockerfile` 复制两个 Nginx 模板，`EXPOSE 8080 8443`，健康检查访问 `8080`。
- 当前 `deploy/allinone/entrypoint.sh` 会检测 `/etc/nginx/certs/fullchain.pem` 和 `privkey.pem` 来切换 HTTP/HTTPS 模板。
- 当前 `deploy/nginx/templates/default.conf.template` 是 HTTPS 模式，`default-http.conf.template` 是 HTTP 模式；本任务应收敛为一个 HTTP 单入口模板。
- 相关规范：`.trellis/spec/guides/documentation-truth-guide.md`、`.trellis/spec/backend/logging-guidelines.md`、`.trellis/spec/backend/quality-guidelines.md`。
