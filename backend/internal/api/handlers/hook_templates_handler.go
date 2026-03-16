package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// HookTemplate 内置 hook 模板（备份前/后脚本）
type HookTemplate struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	PreHook     string `json:"pre_hook"`
	PostHook    string `json:"post_hook"`
	Description string `json:"description"`
}

var builtinHookTemplates = []HookTemplate{
	{
		ID:          "mysql",
		Name:        "MySQL 全量 Dump",
		PreHook:     "mysqldump -u root -p\"$MYSQL_ROOT_PASSWORD\" --all-databases --single-transaction > /tmp/xirang-mysql.sql",
		PostHook:    "rm -f /tmp/xirang-mysql.sql",
		Description: "备份前 dump 所有 MySQL 数据库到 /tmp，备份后清理临时文件",
	},
	{
		ID:          "postgres",
		Name:        "PostgreSQL 全量 Dump",
		PreHook:     "su - postgres -c 'pg_dumpall > /tmp/xirang-pg.sql'",
		PostHook:    "rm -f /tmp/xirang-pg.sql",
		Description: "备份前 dump 所有 PostgreSQL 数据库，备份后清理",
	},
	{
		ID:          "mongodb",
		Name:        "MongoDB Dump",
		PreHook:     "mongodump --out /tmp/xirang-mongo",
		PostHook:    "rm -rf /tmp/xirang-mongo",
		Description: "备份前执行 mongodump，备份后清理",
	},
	{
		ID:          "redis",
		Name:        "Redis RDB 快照",
		PreHook:     "redis-cli BGSAVE && sleep 2 && cp /var/lib/redis/dump.rdb /tmp/xirang-redis.rdb",
		PostHook:    "rm -f /tmp/xirang-redis.rdb",
		Description: "备份前触发 Redis BGSAVE 并复制 RDB 文件，备份后清理",
	},
	{
		ID:          "docker-stop",
		Name:        "Docker 容器暂停",
		PreHook:     "docker compose -f /path/to/docker-compose.yml stop",
		PostHook:    "docker compose -f /path/to/docker-compose.yml start",
		Description: "备份前停止容器，备份后重新启动",
	},
}

// HookTemplatesHandler 处理内置 hook 模板的查询
type HookTemplatesHandler struct{}

func NewHookTemplatesHandler() *HookTemplatesHandler {
	return &HookTemplatesHandler{}
}

// List 返回所有内置 hook 模板
func (h *HookTemplatesHandler) List(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"data": builtinHookTemplates})
}
