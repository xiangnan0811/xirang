# Research: SFTP 远程路径符号链接校验

- **Query**: 给 `validateNodePath()` 增加远程符号链接逃逸防御
- **Scope**: 内部 + 外部（pkg/sftp v1.13.10 + sftpgo / restic 实现 + SFTP 协议规范）
- **Date**: 2026-05-03

## 当前实现回顾（必读）

`backend/internal/api/handlers/file_handler.go:305-339` 的 `validateNodePath()`：

```go
clean := filepath.Clean(rawPath)
roots := []string{...}        // node.BasePath + 该节点 tasks 的 RsyncSource
for _, root := range roots {
  if root == "/" || strings.HasPrefix(clean, root+"/") || clean == root {
    return clean, nil           // 仅做字符串前缀比较，未触发任何 RTT
  }
}
```

对比同文件 `validateLocalPath()`（行 342-371）已经使用 `filepath.EvalSymlinks` 做了**两次**解析：先解 join 后路径，再解 base，比较绝对路径前缀。**远程版本缺少等价的"解析后再校验"步骤。**

攻击者只要在节点上能写入 BasePath 内任意路径（合法的备份操作就能做到），就可以 `ln -s /etc /backup/sneaky`，然后 `GET /api/v1/nodes/:id/files?path=/backup/sneaky/passwd` —— 字符串前缀通过，SFTP `Open` 实际打开 `/etc/passwd`。

## pkg/sftp v1.13.10 提供的 API

源码位置：`~/go/pkg/mod/github.com/pkg/sftp@v1.13.10/client.go`

| 方法 | 行号 | 行为 | RTT |
|---|---|---|---|
| `Stat(p)` | 459 | 跟随符号链接，返回 referent 信息 | 1 |
| `Lstat(p)` | 469 | 不跟随符号链接，返回 link 自身信息（含 `os.ModeSymlink`） | 1 |
| `ReadLink(p)` | 498 | 读取一层 symlink target（可能是相对路径） | 1 |
| `RealPath(p)` | 934 | **服务端**做 canonicalize，调用 `SSH_FXP_REALPATH`（SFTP v3 标准包，OpenSSH 默认实现），返回服务端解析后的绝对路径 | 1 |

> 注：`ReadLink` 内部用 `sshFxpReadlinkPacket`，`RealPath` 用 `sshFxpRealpathPacket`。两者都是 SFTP v3 协议规定，OpenSSH server 全量支持。

`sftp.Open()` 没有 "do not follow symlink" 标志位（SFTP v3 协议 OPEN 包只有 READ/WRITE/APPEND/CREAT/TRUNC/EXCL）。所以"在 Open 阶段拒绝跟随符号链接"在协议层做不到——必须在 Open 之前做路径校验。

## SFTP 协议本身的相关说明

- SFTP v3（draft-ietf-secsh-filexfer-02）规定 `SSH_FXP_REALPATH` (op 16) 必须由 server 实现，作用是把任意路径 canonicalize 为绝对路径，**通常会解析符号链接**（OpenSSH 行为：取决于扩展，默认是 `realpath(3)` 语义，全部解析）。
- 没有 LSETSTAT / LOPEN 之类禁止跟随符号链接的标准操作（个别 server 有 `lsetstat@openssh.com` 扩展，但 pkg/sftp 客户端未默认暴露）。

## 同类项目实现参考

### sftpgo（github.com/drakkan/sftpgo, MIT, 主流自托管 SFTP server）

`internal/vfs/osfs.go:380-413`：
```go
func (fs *OsFs) ResolvePath(virtualPath string) (string, error) {
  r := filepath.Clean(filepath.Join(fs.rootDir, virtualPath))
  p, err := filepath.EvalSymlinks(r)        // 解析所有 symlink
  if isNotExist {
    fs.findFirstExistingDir(r)              // 路径不存在时向上找
  }
  return r, fs.isSubDir(p)                  // 校验解析结果仍在 rootDir 内
}
```
另有 `RealPath()` 自己手写 readlink 循环（最多 10 跳），用于不能用 `EvalSymlinks` 的场景。**核心原则：解析所有 symlink 后再做 prefix 校验。**

### restic SFTP backend（github.com/restic/restic）

`internal/backend/sftp/sftp.go` 完全不做路径白名单校验——它信任 backend 完全独占某目录。**不适用我们这种"用户可访问任意 BasePath 子树"场景。**

### Filezilla / VSCode SFTP 插件

属于客户端工具，不做服务端鉴权。无参考价值。

## 性能成本

每个 file_handler 请求路径深度 N，新增 RTT 数：

| 方案 | 新增 RTT | LAN 节点 (~5ms RTT) | 跨网节点 (~50ms RTT) |
|---|---|---|---|
| A. 单次 RealPath | 1 | +5ms | +50ms |
| B. 逐级 Lstat + ReadLink 自解析 | N（路径深度，典型 4-6） | +20-30ms | +200-300ms |
| C. EvalSymlinks 等价的多跳 readlink | N + symlink 跳数 | +30ms | +300ms |

当前接口 `ListNodeFiles` 已经有 `dialSFTP`（含 SSH 握手 ~100-300ms）+ `ReadDir`（1 RTT）。新增 1 RTT 不显著；新增 N 次显著。

## 修复方案

### 方案 A：调用 SFTP `RealPath` 一次解析（推荐）

```go
// 拿到原始 clean 后，先 RealPath 解析
realPath, err := sftpClient.RealPath(clean)
if err != nil {
  // 路径不存在或权限不足
  return "", err
}
// 用 RealPath 结果做白名单校验
for _, root := range roots {
  if root == "/" || strings.HasPrefix(realPath, root+"/") || realPath == root {
    return realPath, nil   // 注意返回 realPath 而不是 clean，后续 Open 用解析后的路径
  }
}
return "", fmt.Errorf("路径超出允许范围")
```

需要重构：`validateNodePath` 签名增加 `*sftp.Client` 参数。所以调用顺序变成 `dialSFTP → validateNodePath(..., sftpClient) → listSFTPDir`。或新增一个独立函数 `resolveAndValidateNodePath(client, raw, node, db)`。

**Pros**: 1 RTT；服务端权威解析（包括相对 symlink、`..`、多跳）；和 sftpgo `RealPath` 思路一致；服务端没权限的目录会直接报错。
**Cons**: 依赖 server 正确实现 REALPATH；如果 root 自己是 symlink（如 `/data` → `/mnt/data`），需要把 roots 也用 `RealPath` 解析后再比较，否则永远不命中（必做，否则 false negative）。
**额外工作**: 把 roots 也跑一次 `RealPath`，结果可在 dialSFTP 后缓存（sync.Map by node.ID + 短 TTL，或单次请求内复用）。

### 方案 B：逐级 Lstat + 显式拒绝符号链接

```go
parts := strings.Split(strings.TrimPrefix(clean, "/"), "/")
cur := "/"
for _, p := range parts {
  cur = path.Join(cur, p)
  info, err := sftpClient.Lstat(cur)
  if err != nil { return "", err }
  if info.Mode()&os.ModeSymlink != 0 {
    return "", fmt.Errorf("路径包含符号链接，已拒绝: %s", cur)
  }
}
// 通过逐级检查后再做 prefix 校验
```

**Pros**: 完全不允许 symlink，最严格；不依赖 RealPath；规则极简单。
**Cons**: N RTT，深路径慢；用户合法的 symlink（例如运维方便的 `/data` → `/mnt/data`）无法访问，需要用户改配置；可能需要 `FILE_BROWSER_ALLOW_SYMLINK` 开关回退。
**适用场景**: 安全要求极高、deployment 路径都是真实目录的场景。

### 方案 C：EvalSymlinks 风格的 ReadLink 循环（自解析）

类似 sftpgo `RealPath()` 函数（`osfs.go:380-413`），不依赖服务端 REALPATH，自己 Lstat → ReadLink → join → 重试，最多 10 跳。

**Pros**: 不依赖服务端 REALPATH；行为可预测可日志化；可在解析过程中加白名单"边界检查"（每跳后立即做 prefix 校验，发现越界立即拒绝）。
**Cons**: 代码量最大；RTT 最多（N + symlink 链长度）；要小心循环引用、绝对/相对路径处理。
**适用场景**: 服务端是异构 SFTP 实现、不信任 REALPATH 的场景。我们的目标节点都是标准 OpenSSH，**不必要走这条**。

## 推荐路径

主代理优先选 **方案 A**，理由：

1. 1 RTT 影响可忽略；
2. OpenSSH 默认 server 必定支持 `SSH_FXP_REALPATH`；
3. 与已有 `validateLocalPath` 对称（本地用 `filepath.EvalSymlinks`，远程用 `sftp.RealPath`）；
4. 可保留用户 symlink 的可用性，不破坏运维习惯；
5. **必须**同时把 roots 也做一次 `RealPath`（或用同一会话内缓存），否则 root 是 symlink 时永远拒绝。

如果主代理出于安全偏好（"白名单内绝对禁止 symlink"），则降级到方案 B，并需要文档说明用户 BasePath 必须是真实目录。

## Caveats / Not Found

- 没有验证 `pkg/sftp` 在 OpenSSH server 早期版本（< 7.0）下 `RealPath` 是否对不存在路径有兼容差异。OpenSSH ≥ 7.0 行为可控；老版本可能返回 SSH_FX_NO_SUCH_FILE。建议在实现里捕获 `os.IsNotExist` 并对"目录还未创建"场景给出明确错误（而不是当 symlink 攻击拒绝）。
- 未实测 SFTP `RealPath` 对相对路径（如 `..`）的处理在所有 server 上一致；建议在调用前先 `filepath.Clean`，绝对路径再传入。
- 没有覆盖 `FILE_BROWSER_ALLOW_ALL=true` 开发模式分支——方案 A/B 都应保留这个绕过开关，但只在 `IsDevelopmentEnv()` 下生效（现有 307-312 行已有保护）。
