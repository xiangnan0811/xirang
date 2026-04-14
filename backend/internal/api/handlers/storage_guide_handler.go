package handlers

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/gin-gonic/gin"
)

// StorageGuideHandler 处理外部存储挂载引导相关操作
type StorageGuideHandler struct{}

func NewStorageGuideHandler() *StorageGuideHandler {
	return &StorageGuideHandler{}
}

type verifyMountRequest struct {
	Path string `json:"path" binding:"required"`
}

type verifyMountResult struct {
	Exists       bool   `json:"exists"`
	IsMountPoint bool   `json:"is_mount_point"`
	Writable     bool   `json:"writable"`
	TotalGB      uint64 `json:"total_gb"`
	FreeGB       uint64 `json:"free_gb"`
	Filesystem   string `json:"filesystem"`
}

// 禁止验证的系统关键路径
var forbiddenPaths = []string{"/", "/etc", "/usr", "/var", "/boot", "/sys", "/proc", "/dev", "/bin", "/sbin", "/lib"}

// VerifyMount 验证指定路径是否为有效的挂载点
func (h *StorageGuideHandler) VerifyMount(c *gin.Context) {
	var req verifyMountRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondBadRequest(c, "请提供要验证的路径")
		return
	}

	mountPath := filepath.Clean(req.Path)

	// 安全校验：必须是绝对路径
	if !filepath.IsAbs(mountPath) {
		respondBadRequest(c, "路径必须是绝对路径")
		return
	}

	// 安全校验：禁止路径遍历
	if strings.Contains(req.Path, "..") {
		respondBadRequest(c, "路径不能包含 ..")
		return
	}

	// 安全校验：禁止系统关键路径
	for _, forbidden := range forbiddenPaths {
		if mountPath == forbidden || strings.HasPrefix(mountPath, forbidden+"/") {
			respondBadRequest(c, fmt.Sprintf("不允许验证系统路径: %s", mountPath))
			return
		}
	}

	result := verifyMountResult{}

	// 1. 检查路径是否存在
	info, err := os.Stat(mountPath)
	if err != nil {
		respondOK(c, result)
		return
	}
	if !info.IsDir() {
		respondBadRequest(c, "指定路径不是目录")
		return
	}
	result.Exists = true

	// 2. 检查是否为挂载点（比较当前目录与父目录的设备 ID）
	var pathStat, parentStat syscall.Stat_t
	if err := syscall.Stat(mountPath, &pathStat); err == nil {
		parentPath := filepath.Dir(mountPath)
		if err := syscall.Stat(parentPath, &parentStat); err == nil {
			result.IsMountPoint = pathStat.Dev != parentStat.Dev
		}
	}

	// 3. 检查是否可写（创建并删除临时文件）
	if f, err := os.CreateTemp(mountPath, ".xirang_write_test_*"); err == nil {
		tmpName := f.Name()
		f.Close()          //nolint:errcheck
		os.Remove(tmpName) //nolint:errcheck
		result.Writable = true
	}

	// 4. 获取磁盘空间信息（平台相关，见 storage_guide_*_.go）
	fillDiskInfo(mountPath, &result)

	respondOK(c, result)
}
