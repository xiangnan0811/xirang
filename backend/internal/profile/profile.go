package profile

import (
	"bytes"
	"fmt"
	"text/template"
)

// ProfileDefinition 定义了一个应用感知备份 profile 的完整信息。
type ProfileDefinition struct {
	ID              string        `json:"id"`
	Name            string        `json:"name"`
	Description     string        `json:"description"`
	CredentialType  string        `json:"credential_type"` // 期望的 AppCredential.Type
	IsDocker        bool          `json:"is_docker"`
	ConfigSchema    []ConfigField `json:"config_schema"`
	// 模板字段不暴露给 API（安全预防）
	PreHookTemplate  string `json:"-"`
	PostHookTemplate string `json:"-"`
}

// ConfigField 描述 credential config 中的一个字段，供前端动态渲染表单。
type ConfigField struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Type        string `json:"type"` // text, password, number
	Required    bool   `json:"required"`
	Placeholder string `json:"placeholder,omitempty"`
}

// hostConfigSchema 主机类 profile 的通用 config schema。
var hostConfigSchema = []ConfigField{
	{Key: "host", Label: "主机地址", Type: "text", Required: true, Placeholder: "127.0.0.1"},
	{Key: "port", Label: "端口", Type: "text", Required: false, Placeholder: "默认端口"},
	{Key: "user", Label: "用户名", Type: "text", Required: false, Placeholder: "数据库用户"},
	{Key: "password", Label: "密码", Type: "password", Required: false, Placeholder: "数据库密码"},
}

// dockerConfigSchema 容器类 profile 的通用 config schema。
var dockerConfigSchema = []ConfigField{
	{Key: "container_name", Label: "容器名称", Type: "text", Required: true, Placeholder: "my-mysql-container"},
	{Key: "user", Label: "用户名", Type: "text", Required: false, Placeholder: "数据库用户"},
	{Key: "password", Label: "密码", Type: "password", Required: false, Placeholder: "数据库密码"},
}

// BuiltinProfiles 包含所有 8 个内置 profile（4 主机 + 4 容器化）。
var BuiltinProfiles = []ProfileDefinition{
	{
		ID:             "mysql",
		Name:           "MySQL 全量 Dump",
		Description:    "mysqldump --single-transaction 导出所有数据库",
		CredentialType: "mysql",
		IsDocker:       false,
		ConfigSchema:   hostConfigSchema,
		PreHookTemplate: `mysqldump{{if .user}} -u {{.user}}{{end}}{{if .password}} -p'{{.password}}'{{end}}{{if .host}} -h {{.host}}{{end}}{{if .port}} -P {{.port}}{{end}} --all-databases --single-transaction --routines --triggers > /tmp/xirang-mysql-backup.sql`,
		PostHookTemplate: `rm -f /tmp/xirang-mysql-backup.sql`,
	},
	{
		ID:             "postgres",
		Name:           "PostgreSQL 全量 Dump",
		Description:    "pg_dumpall 导出所有数据库（su to postgres）",
		CredentialType: "postgres",
		IsDocker:       false,
		ConfigSchema:   hostConfigSchema,
		PreHookTemplate: `{{if .password}}PGPASSWORD='{{.password}}' {{end}}su - postgres -c 'pg_dumpall{{if .host}} -h {{.host}}{{end}}{{if .port}} -p {{.port}}{{end}}{{if .user}} -U {{.user}}{{end}} > /tmp/xirang-pg-backup.sql'`,
		PostHookTemplate: `rm -f /tmp/xirang-pg-backup.sql`,
	},
	{
		ID:             "mongodb",
		Name:           "MongoDB Dump",
		Description:    "mongodump 导出所有集合",
		CredentialType: "mongodb",
		IsDocker:       false,
		ConfigSchema:   hostConfigSchema,
		PreHookTemplate: `mongodump{{if .host}} --host {{.host}}{{end}}{{if .port}} --port {{.port}}{{end}}{{if .user}} --username {{.user}}{{end}}{{if .password}} --password '{{.password}}'{{end}} --out /tmp/xirang-mongo-backup`,
		PostHookTemplate: `rm -rf /tmp/xirang-mongo-backup`,
	},
	{
		ID:             "redis",
		Name:           "Redis RDB 快照",
		Description:    "BGSAVE + 复制 RDB 文件",
		CredentialType: "redis",
		IsDocker:       false,
		ConfigSchema:   hostConfigSchema,
		PreHookTemplate: `redis-cli{{if .host}} -h {{.host}}{{end}}{{if .port}} -p {{.port}}{{end}}{{if .password}} -a '{{.password}}'{{end}} BGSAVE && sleep 2 && cp /var/lib/redis/dump.rdb /tmp/xirang-redis-backup.rdb`,
		PostHookTemplate: `rm -f /tmp/xirang-redis-backup.rdb`,
	},
	{
		ID:             "docker-mysql",
		Name:           "Docker MySQL Dump",
		Description:    "docker exec mysqldump --single-transaction 导出所有数据库",
		CredentialType: "docker-mysql",
		IsDocker:       true,
		ConfigSchema:   dockerConfigSchema,
		PreHookTemplate: `docker inspect {{.container_name}} >/dev/null 2>&1 || { echo "容器 {{.container_name}} 不存在或未运行"; exit 1; } && docker exec {{.container_name}} mysqldump{{if .user}} -u {{.user}}{{end}}{{if .password}} -p'{{.password}}'{{end}} --all-databases --single-transaction --routines --triggers > /tmp/xirang-docker-mysql-backup.sql`,
		PostHookTemplate: `rm -f /tmp/xirang-docker-mysql-backup.sql`,
	},
	{
		ID:             "docker-postgres",
		Name:           "Docker PostgreSQL Dump",
		Description:    "docker exec pg_dumpall 导出所有数据库",
		CredentialType: "docker-postgres",
		IsDocker:       true,
		ConfigSchema:   dockerConfigSchema,
		PreHookTemplate: `docker inspect {{.container_name}} >/dev/null 2>&1 || { echo "容器 {{.container_name}} 不存在或未运行"; exit 1; } && docker exec {{.container_name}} pg_dumpall{{if .user}} -U {{.user}}{{end}} > /tmp/xirang-docker-pg-backup.sql`,
		PostHookTemplate: `rm -f /tmp/xirang-docker-pg-backup.sql`,
	},
	{
		ID:             "docker-mongodb",
		Name:           "Docker MongoDB Dump",
		Description:    "docker exec mongodump 导出所有集合",
		CredentialType: "docker-mongodb",
		IsDocker:       true,
		ConfigSchema:   dockerConfigSchema,
		PreHookTemplate: `docker inspect {{.container_name}} >/dev/null 2>&1 || { echo "容器 {{.container_name}} 不存在或未运行"; exit 1; } && docker exec {{.container_name}} mongodump{{if .user}} --username {{.user}}{{end}}{{if .password}} --password '{{.password}}'{{end}} --out /tmp/xirang-docker-mongo-backup`,
		PostHookTemplate: `rm -rf /tmp/xirang-docker-mongo-backup`,
	},
	{
		ID:             "docker-redis",
		Name:           "Docker Redis RDB 快照",
		Description:    "docker exec redis-cli BGSAVE + docker cp RDB 文件",
		CredentialType: "docker-redis",
		IsDocker:       true,
		ConfigSchema:   dockerConfigSchema,
		PreHookTemplate: `docker inspect {{.container_name}} >/dev/null 2>&1 || { echo "容器 {{.container_name}} 不存在或未运行"; exit 1; } && docker exec {{.container_name}} redis-cli{{if .password}} -a '{{.password}}'{{end}} BGSAVE && sleep 2 && docker cp {{.container_name}}:/data/dump.rdb /tmp/xirang-docker-redis-backup.rdb`,
		PostHookTemplate: `rm -f /tmp/xirang-docker-redis-backup.rdb`,
	},
}

// GetProfile 按 ID 查找 profile 定义。
func GetProfile(id string) (*ProfileDefinition, bool) {
	for i := range BuiltinProfiles {
		if BuiltinProfiles[i].ID == id {
			return &BuiltinProfiles[i], true
		}
	}
	return nil, false
}

// ListProfiles 返回所有内置 profile 的公开信息（不含模板）。
func ListProfiles() []ProfileDefinition {
	return BuiltinProfiles
}

// RenderHooks 根据 profile ID 和 credential config 渲染 pre-hook / post-hook。
// config 应为已解密的 AppCredential.Config JSON map，包含 host/port/user/password/container_name 等键。
func RenderHooks(profileID string, config map[string]interface{}) (preHook string, postHook string, err error) {
	p, ok := GetProfile(profileID)
	if !ok {
		return "", "", fmt.Errorf("未知的 profile: %s", profileID)
	}
	preHook, err = renderTemplate(p.PreHookTemplate, config)
	if err != nil {
		return "", "", fmt.Errorf("渲染 pre-hook 失败: %w", err)
	}
	postHook, err = renderTemplate(p.PostHookTemplate, config)
	if err != nil {
		return "", "", fmt.Errorf("渲染 post-hook 失败: %w", err)
	}
	return preHook, postHook, nil
}

// renderTemplate 使用 Go text/template 渲染模板字符串。
func renderTemplate(tmpl string, data map[string]interface{}) (string, error) {
	t, err := template.New("hook").Parse(tmpl)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}
