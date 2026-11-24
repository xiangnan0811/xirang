//go:build linux

package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"unsafe"

	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"golang.org/x/sys/unix"
)

func TestStorageUsageDeduplicatesBtrfsSubvolumesByBackingSuperblock(t *testing.T) {
	// 使用真实 Linux Btrfs /proc/self/mountinfo 格式：/ (@, st_dev=30, f_fsid=...67)
	// 与 /home (@home, st_dev=46, f_fsid=...66) 共享同一超级块 0:28。
	sampleMountInfo := strings.Join([]string{
		"30 1 0:28 /@ / rw,relatime shared:1 - btrfs /dev/nvme0n1p2 rw,compress=zstd:3,subvolid=256,subvol=/@",
		"46 30 0:28 /@home /home rw,relatime shared:2 - btrfs /dev/nvme0n1p2 rw,compress=zstd:3,subvolid=257,subvol=/@home",
		"55 30 259:3 / /mnt/ext4\\040drive rw,relatime shared:3 - ext4 /dev/sdb1 rw",
		"60 30 0:90 / /mnt/shadow rw,relatime - tmpfs tmpfs rw",
		"61 60 0:91 / /mnt/shadow rw,relatime - tmpfs tmpfs rw",
	}, "\n")
	table := parseMountInfo(strings.NewReader(sampleMountInfo))

	sbRootByID, ok := table.superDevForMountID(30)
	if !ok || sbRootByID != "0:28" {
		t.Fatalf("mountID=30 应解析为超级块 0:28，实际: %q (%v)", sbRootByID, ok)
	}
	sbHomeByID, ok := table.superDevForMountID(46)
	if !ok || sbHomeByID != "0:28" {
		t.Fatalf("mountID=46 应解析为同一超级块 0:28，实际: %q (%v)", sbHomeByID, ok)
	}
	if sbRootByID != sbHomeByID {
		t.Fatalf("同一 Btrfs 文件系统的 / 与 /home 必须解析出相同超级块键")
	}
	sbRootByPath, _ := table.superDevForPath("/backup/www")
	sbHomeByPath, _ := table.superDevForPath("/home/backups/app")
	if sbRootByPath != "0:28" || sbHomeByPath != "0:28" {
		t.Fatalf("按路径匹配 Btrfs 子卷应返回相同超级块 0:28，实际 root=%q home=%q", sbRootByPath, sbHomeByPath)
	}
	sbEscaped, ok := table.superDevForPath("/mnt/ext4 drive/backups")
	if !ok || sbEscaped != "259:3" {
		t.Fatalf("含八进制转义空格的挂载点应正确匹配 259:3，实际: %q (%v)", sbEscaped, ok)
	}
	sbShadow, ok := table.superDevForPath("/mnt/shadow/data")
	if !ok || sbShadow != "0:91" {
		t.Fatalf("被遮蔽挂载点应命中后挂载的 0:91，实际: %q (%v)", sbShadow, ok)
	}

	// 端到端验证：两个不同 Btrfs 子卷（不同 st_dev 和不同 f_fsid，但共享 mountinfo 0:28）只输出 1 条整卷容量，
	// 而独立 ext4 卷保留为第 2 条。
	db := openStorageUsageTestDB(t)
	policies := []model.Policy{
		{Name: "btrfs-root", SourcePath: "/src/1", TargetPath: "/backup/root-subvol", CronSpec: "0 * * * *", RetentionMode: "simple", Enabled: true},
		{Name: "btrfs-home", SourcePath: "/src/2", TargetPath: "/home/backup-subvol", CronSpec: "0 * * * *", RetentionMode: "simple", Enabled: true},
		{Name: "ext4-data", SourcePath: "/src/3", TargetPath: "/mnt/ext4 drive/backup", CronSpec: "0 * * * *", RetentionMode: "simple", Enabled: true},
	}
	for i := range policies {
		if err := model.CreatePolicyWithExplicitValues(db, &policies[i], model.PolicyCreateExplicitColumns()...); err != nil {
			t.Fatalf("创建策略失败: %v", err)
		}
	}

	h := NewStorageUsageHandler(db)
	h.statMount = func(path string) (string, unix.Statfs_t, error) {
		sb, ok := table.superDevForPath(path)
		if !ok {
			return "", unix.Statfs_t{}, os.ErrNotExist
		}
		switch path {
		case "/backup/root-subvol":
			return "sb:" + sb, unix.Statfs_t{
				Type: 0x9123683e, Bsize: 4096, Blocks: 26214400, Bavail: 13107200,
				Fsid: unix.Fsid{Val: [2]int32{0x7666a888, -0x45813499}},
			}, nil
		case "/home/backup-subvol":
			return "sb:" + sb, unix.Statfs_t{
				Type: 0x9123683e, Bsize: 4096, Blocks: 26214400, Bavail: 13107200,
				Fsid: unix.Fsid{Val: [2]int32{0x7666a888, -0x4581349a}},
			}, nil
		default:
			return "sb:" + sb, unix.Statfs_t{
				Type: 0xef53, Bsize: 4096, Blocks: 52428800, Bavail: 26214400,
				Fsid: unix.Fsid{Val: [2]int32{0x77777777, 0x11111111}},
			}, nil
		}
	}

	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.CtxRole, "admin")
		c.Set(middleware.CtxUserID, uint(1))
		c.Next()
	})
	r.GET("/overview/storage-usage", h.Get)

	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/overview/storage-usage", nil))
	if resp.Code != http.StatusOK {
		t.Fatalf("期望 200，实际: %d body=%s", resp.Code, resp.Body.String())
	}
	var body struct {
		Data struct {
			MountPoints []mountPointInfo `json:"mount_points"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if len(body.Data.MountPoints) != 2 {
		t.Fatalf("同 Btrfs 超级块不同 f_fsid 的两个子卷应去重为 1 条，连同独立卷共 2 条，实际: %+v", body.Data.MountPoints)
	}
	if body.Data.MountPoints[0].Path != "/backup/root-subvol" || body.Data.MountPoints[1].Path != "/mnt/ext4 drive/backup" {
		t.Fatalf("挂载点顺序与去重结果不符合预期: %+v", body.Data.MountPoints)
	}

	// 独立通过内核 BTRFS_IOC_FS_INFO ioctl 获取 128 位 Btrfs 文件系统 UUID，
	// 不依赖容量相等或生产 mountinfo 解析，准确验证同卷收敛与异卷区分。
	if wd, err := os.Getwd(); err == nil {
		etcUUID, etcOK := btrfsFilesystemUUID("/etc")
		wdUUID, wdOK := btrfsFilesystemUUID(wd)
		if etcOK && wdOK {
			liveStatter := newDefaultMountVolumeStatter()
			k1, _, err1 := liveStatter("/etc")
			k2, _, err2 := liveStatter(wd)
			if err1 != nil || err2 != nil {
				t.Fatalf("真实 Btrfs 子卷探测失败: err1=%v err2=%v", err1, err2)
			}
			if etcUUID == wdUUID {
				if !strings.HasPrefix(k1, "sb:") || k1 != k2 {
					t.Fatalf("同一 Btrfs UUID (%x) 下的 /etc 与 %s 应解析为相同超级块键，实际 k1=%q k2=%q", etcUUID, wd, k1, k2)
				}
				t.Logf("verified live Btrfs subvolumes /etc and %s share kernel UUID %x and deduplicate to %s", wd, etcUUID, k1)
			} else if k1 == k2 {
				t.Fatalf("不同 Btrfs UUID (%x vs %x) 不得去重为同一键: %q", etcUUID, wdUUID, k1)
			}
		}
	}
}

func btrfsFilesystemUUID(path string) ([16]byte, bool) {
	// _IOR(BTRFS_IOCTL_MAGIC=0x94, 31, struct btrfs_ioctl_fs_info_args[1024])
	const btrfsIocFsInfo = 0x8400941f
	var zero [16]byte
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return zero, false
	}
	defer func() { _ = unix.Close(fd) }()

	var buf [1024]byte
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), btrfsIocFsInfo, uintptr(unsafe.Pointer(&buf[0])))
	if errno != 0 {
		return zero, false
	}
	var fsid [16]byte
	copy(fsid[:], buf[16:32])
	return fsid, fsid != zero
}
