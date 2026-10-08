package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newCronBackupSQLiteDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "xirang.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开 SQLite 测试数据库失败: %v", err)
	}
	return db
}

func cronBackupStatusRequest(t *testing.T, handler *SystemHandler, now time.Time) CronBackupStatusResponse {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/v1/system/cron-backup-status", nil)
	handler.now = func() time.Time { return now }
	handler.CronBackupStatus(ctx)
	if recorder.Code != http.StatusOK {
		t.Fatalf("cron 状态响应状态码 = %d，want %d", recorder.Code, http.StatusOK)
	}
	var envelope struct {
		Code int                      `json:"code"`
		Data CronBackupStatusResponse `json:"data"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&envelope); err != nil {
		t.Fatalf("解析 cron 状态响应失败: %v", err)
	}
	if envelope.Code != http.StatusOK {
		t.Fatalf("cron 状态响应 code = %d，want %d", envelope.Code, http.StatusOK)
	}
	return envelope.Data
}

func cronBackupArtifactName(engine string, at time.Time) string {
	if engine == "postgres" {
		return "xirang-postgres-" + at.Format("20060102-150405") + ".dump"
	}
	return "xirang-sqlite-" + at.Format("20060102-150405") + ".db"
}

func writeCronBackupPair(t *testing.T, directory, engine string, at time.Time, recordPath string) (string, string) {
	t.Helper()
	name := cronBackupArtifactName(engine, at)
	artifactPath := filepath.Join(directory, name)
	checksumPath := artifactPath + ".sha256"
	if recordPath == "" {
		recordPath = artifactPath
	}
	if err := os.WriteFile(artifactPath, []byte("FAKE_BACKUP_ARTIFACT_FOR_TEST_ONLY"), 0o600); err != nil {
		t.Fatalf("写入 cron 产物失败: %v", err)
	}
	record := strings.Repeat("a", 64) + "  " + recordPath + "\n"
	if err := os.WriteFile(checksumPath, []byte(record), 0o600); err != nil {
		t.Fatalf("写入 cron 校验文件失败: %v", err)
	}
	if err := os.Chtimes(artifactPath, at, at); err != nil {
		t.Fatalf("设置 cron 产物时间失败: %v", err)
	}
	if err := os.Chtimes(checksumPath, at, at); err != nil {
		t.Fatalf("设置 cron 校验文件时间失败: %v", err)
	}
	return artifactPath, checksumPath
}

func TestCronBackupStatusAllStatuses(t *testing.T) {
	now := time.Date(2026, 10, 7, 3, 0, 0, 0, time.UTC)

	t.Run("not configured", func(t *testing.T) {
		t.Setenv("CRON_DB_BACKUP_DIR", "")
		t.Setenv("CRON_DB_BACKUP_MAX_AGE_HOURS", "")
		got := cronBackupStatusRequest(t, NewSystemHandler(newCronBackupSQLiteDB(t)), now)
		if got.Status != cronBackupStatusNotConfigured || got.Engine != "sqlite" {
			t.Fatalf("状态 = %+v", got)
		}
		if got.MaxAgeSeconds != 26*60*60 || got.LatestCompleteAt != "" || got.ArtifactName != "" {
			t.Fatalf("默认未配置响应 = %+v", got)
		}
	})

	t.Run("invalid configuration", func(t *testing.T) {
		t.Setenv("CRON_DB_BACKUP_DIR", t.TempDir())
		t.Setenv("CRON_DB_BACKUP_MAX_AGE_HOURS", "8761")
		got := cronBackupStatusRequest(t, NewSystemHandler(newCronBackupSQLiteDB(t)), now)
		if got.Status != cronBackupStatusInvalidConfiguration || got.MaxAgeSeconds != 0 {
			t.Fatalf("无效配置响应 = %+v", got)
		}
	})

	t.Run("unknown engine", func(t *testing.T) {
		t.Setenv("CRON_DB_BACKUP_DIR", t.TempDir())
		t.Setenv("CRON_DB_BACKUP_MAX_AGE_HOURS", "26")
		got := cronBackupStatusRequest(t, NewSystemHandler(nil), now)
		if got.Status != cronBackupStatusInvalidConfiguration || got.Engine != "" {
			t.Fatalf("未知引擎响应 = %+v", got)
		}
	})

	t.Run("directory unreadable", func(t *testing.T) {
		directory := filepath.Join(t.TempDir(), "missing")
		t.Setenv("CRON_DB_BACKUP_DIR", directory)
		t.Setenv("CRON_DB_BACKUP_MAX_AGE_HOURS", "26")
		got := cronBackupStatusRequest(t, NewSystemHandler(newCronBackupSQLiteDB(t)), now)
		if got.Status != cronBackupStatusDirectoryUnreadable || got.Directory != directory {
			t.Fatalf("目录不可读响应 = %+v", got)
		}
	})

	t.Run("no complete backup", func(t *testing.T) {
		directory := t.TempDir()
		t.Setenv("CRON_DB_BACKUP_DIR", directory)
		t.Setenv("CRON_DB_BACKUP_MAX_AGE_HOURS", "26")
		artifact := filepath.Join(directory, "xirang-sqlite-20261007-020000.db")
		if err := os.WriteFile(artifact, []byte("FAKE_BACKUP_ARTIFACT_FOR_TEST_ONLY"), 0o600); err != nil {
			t.Fatal(err)
		}
		got := cronBackupStatusRequest(t, NewSystemHandler(newCronBackupSQLiteDB(t)), now)
		if got.Status != cronBackupStatusNoCompleteBackup || got.LatestCompleteAt != "" {
			t.Fatalf("无完整备份响应 = %+v", got)
		}
	})

	t.Run("scan limit exceeded", func(t *testing.T) {
		directory := t.TempDir()
		t.Setenv("CRON_DB_BACKUP_DIR", directory)
		t.Setenv("CRON_DB_BACKUP_MAX_AGE_HOURS", "26")
		for index := range cronBackupMaxEntries + 1 {
			name := filepath.Join(directory, "unrelated-"+time.Unix(int64(index), 0).Format("20060102150405"))
			if err := os.WriteFile(name, []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		got := cronBackupStatusRequest(t, NewSystemHandler(newCronBackupSQLiteDB(t)), now)
		if got.Status != cronBackupStatusScanLimitExceeded || got.LatestCompleteAt != "" {
			t.Fatalf("扫描上限响应 = %+v", got)
		}
	})

	t.Run("clock anomaly", func(t *testing.T) {
		directory := t.TempDir()
		t.Setenv("CRON_DB_BACKUP_DIR", directory)
		t.Setenv("CRON_DB_BACKUP_MAX_AGE_HOURS", "26")
		future := now.Add(time.Minute)
		artifact, checksum := writeCronBackupPair(t, directory, "sqlite", future, "")
		if artifact == "" || checksum == "" {
			t.Fatal("未创建完整备份对")
		}
		got := cronBackupStatusRequest(t, NewSystemHandler(newCronBackupSQLiteDB(t)), now)
		if got.Status != cronBackupStatusClockAnomaly || got.ArtifactName != filepath.Base(artifact) {
			t.Fatalf("未来时间响应 = %+v", got)
		}
	})

	t.Run("fresh and boundary stale", func(t *testing.T) {
		directory := t.TempDir()
		t.Setenv("CRON_DB_BACKUP_DIR", directory)
		t.Setenv("CRON_DB_BACKUP_MAX_AGE_HOURS", "1")
		freshArtifact, freshChecksum := writeCronBackupPair(t, directory, "sqlite", now.Add(-time.Hour), "")
		fresh := cronBackupStatusRequest(t, NewSystemHandler(newCronBackupSQLiteDB(t)), now)
		if fresh.Status != cronBackupStatusFresh {
			t.Fatalf("边界新鲜度响应 = %+v", fresh)
		}
		if fresh.CheckedAt != "2026-10-07T03:00:00Z" || fresh.LatestCompleteAt != "2026-10-07T02:00:00Z" {
			t.Fatalf("UTC 时间字段 = %+v", fresh)
		}

		if _, err := os.Stat(filepath.Join(directory, "xirang-sqlite-20261007-020000.db")); err != nil {
			t.Fatal(err)
		}
		older := now.Add(-time.Hour - time.Second)
		artifact := filepath.Join(directory, "xirang-sqlite-20261007-015959.db")
		checksum := artifact + ".sha256"
		if err := os.WriteFile(artifact, []byte("FAKE_BACKUP_ARTIFACT_FOR_TEST_ONLY"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(checksum, []byte(strings.Repeat("a", 64)+"  "+artifact+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(artifact, older, older); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(checksum, older, older); err != nil {
			t.Fatal(err)
		}
		stale := cronBackupStatusRequest(t, NewSystemHandler(newCronBackupSQLiteDB(t)), now)
		if stale.Status != cronBackupStatusFresh {
			t.Fatalf("有更新边界对时状态 = %+v", stale)
		}
		if err := os.Remove(freshArtifact); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(freshChecksum); err != nil {
			t.Fatal(err)
		}
		stale = cronBackupStatusRequest(t, NewSystemHandler(newCronBackupSQLiteDB(t)), now)
		if stale.Status != cronBackupStatusStale {
			t.Fatalf("旧备份状态 = %+v", stale)
		}
	})
}

func TestCronBackupStatusIgnoresDBTypeAndUsesRuntimeDialect(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("DB_TYPE", "postgres")
	t.Setenv("CRON_DB_BACKUP_DIR", directory)
	t.Setenv("CRON_DB_BACKUP_MAX_AGE_HOURS", "26")
	now := time.Date(2026, 10, 7, 3, 0, 0, 0, time.UTC)
	writeCronBackupPair(t, directory, "sqlite", now.Add(-time.Hour), "")
	got := cronBackupStatusRequest(t, NewSystemHandler(newCronBackupSQLiteDB(t)), now)
	if got.Engine != "sqlite" || got.Status != cronBackupStatusFresh {
		t.Fatalf("DB_TYPE 不应覆盖运行时方言，响应 = %+v", got)
	}
}

func TestCronBackupStatusRejectsUnsafeAndIncompletePairs(t *testing.T) {
	now := time.Date(2026, 10, 7, 3, 0, 0, 0, time.UTC)
	cases := []struct {
		name  string
		setup func(t *testing.T, directory string)
	}{
		{
			name: "artifact symlink",
			setup: func(t *testing.T, directory string) {
				outside := filepath.Join(t.TempDir(), "outside.db")
				if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
					t.Fatal(err)
				}
				artifact := filepath.Join(directory, "xirang-sqlite-20261007-020000.db")
				if err := os.Symlink(outside, artifact); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(artifact+".sha256", []byte(strings.Repeat("a", 64)+"  "+artifact+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "artifact directory",
			setup: func(t *testing.T, directory string) {
				artifact := filepath.Join(directory, "xirang-sqlite-20261007-020000.db")
				if err := os.Mkdir(artifact, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(artifact+".sha256", []byte(strings.Repeat("a", 64)+"  "+artifact+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "zero artifact",
			setup: func(t *testing.T, directory string) {
				artifact := filepath.Join(directory, "xirang-sqlite-20261007-020000.db")
				if err := os.WriteFile(artifact, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(artifact+".sha256", []byte(strings.Repeat("a", 64)+"  "+artifact+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "checksum symlink",
			setup: func(t *testing.T, directory string) {
				artifact, checksum := writeCronBackupPair(t, directory, "sqlite", now.Add(-time.Hour), "")
				outside := filepath.Join(t.TempDir(), "outside.sha256")
				if err := os.WriteFile(outside, []byte(strings.Repeat("a", 64)+"  "+artifact+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(checksum); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, checksum); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "checksum directory",
			setup: func(t *testing.T, directory string) {
				artifact, checksum := writeCronBackupPair(t, directory, "sqlite", now.Add(-time.Hour), "")
				if err := os.Remove(checksum); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(checksum, 0o700); err != nil {
					t.Fatal(err)
				}
				_ = artifact
			},
		},
		{
			name: "wrong checksum path",
			setup: func(t *testing.T, directory string) {
				artifact := filepath.Join(directory, "xirang-sqlite-20261007-020000.db")
				if err := os.WriteFile(artifact, []byte("FAKE_BACKUP_ARTIFACT_FOR_TEST_ONLY"), 0o600); err != nil {
					t.Fatal(err)
				}
				wrong := filepath.Join(directory, "other.db")
				if err := os.WriteFile(artifact+".sha256", []byte(strings.Repeat("a", 64)+" *"+wrong+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "extra checksum record",
			setup: func(t *testing.T, directory string) {
				artifact := filepath.Join(directory, "xirang-sqlite-20261007-020000.db")
				if err := os.WriteFile(artifact, []byte("FAKE_BACKUP_ARTIFACT_FOR_TEST_ONLY"), 0o600); err != nil {
					t.Fatal(err)
				}
				record := strings.Repeat("a", 64) + "  " + artifact + "\n" + strings.Repeat("b", 64) + "  " + artifact + "\n"
				if err := os.WriteFile(artifact+".sha256", []byte(record), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "tmp artifact is unmanaged",
			setup: func(t *testing.T, directory string) {
				artifact := filepath.Join(directory, "xirang-sqlite-20261007-020000.db.tmp.99")
				if err := os.WriteFile(artifact, []byte("tmp"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(artifact+".sha256", []byte(strings.Repeat("a", 64)+"  "+artifact+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			directory := t.TempDir()
			t.Setenv("CRON_DB_BACKUP_DIR", directory)
			t.Setenv("CRON_DB_BACKUP_MAX_AGE_HOURS", "26")
			testCase.setup(t, directory)
			got := cronBackupStatusRequest(t, NewSystemHandler(newCronBackupSQLiteDB(t)), now)
			if got.Status != cronBackupStatusNoCompleteBackup {
				t.Fatalf("不安全/不完整产物状态 = %+v", got)
			}
		})
	}
}

func TestCronBackupStatusChecksumBudgets(t *testing.T) {
	now := time.Date(2026, 10, 7, 3, 0, 0, 0, time.UTC)
	t.Run("per file", func(t *testing.T) {
		directory := t.TempDir()
		t.Setenv("CRON_DB_BACKUP_DIR", directory)
		t.Setenv("CRON_DB_BACKUP_MAX_AGE_HOURS", "26")
		artifact, checksum := writeCronBackupPair(t, directory, "sqlite", now.Add(-time.Hour), "")
		if err := os.WriteFile(checksum, append([]byte(strings.Repeat("a", 64)+"  "+artifact+"\n"), make([]byte, cronBackupMaxChecksumBytes)...), 0o600); err != nil {
			t.Fatal(err)
		}
		got := cronBackupStatusRequest(t, NewSystemHandler(newCronBackupSQLiteDB(t)), now)
		if got.Status != cronBackupStatusScanLimitExceeded {
			t.Fatalf("单文件上限状态 = %+v", got)
		}
	})

	t.Run("aggregate", func(t *testing.T) {
		directory := t.TempDir()
		t.Setenv("CRON_DB_BACKUP_DIR", directory)
		t.Setenv("CRON_DB_BACKUP_MAX_AGE_HOURS", "8760")
		for index := range cronBackupMaxReadBytes/cronBackupMaxChecksumBytes + 1 {
			at := now.Add(-time.Duration(index+1) * time.Minute)
			artifact, checksum := writeCronBackupPair(t, directory, "sqlite", at, "")
			payload := make([]byte, cronBackupMaxChecksumBytes)
			copy(payload, []byte(strings.Repeat("a", 64)+"  "+artifact+"\n"))
			if err := os.WriteFile(checksum, payload, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		got := cronBackupStatusRequest(t, NewSystemHandler(newCronBackupSQLiteDB(t)), now)
		if got.Status != cronBackupStatusScanLimitExceeded {
			t.Fatalf("累计读取上限状态 = %+v", got)
		}
	})
}

func TestCronBackupStatusReadOnlyAndCanonicalChecksumPaths(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("CRON_DB_BACKUP_DIR", directory)
	t.Setenv("CRON_DB_BACKUP_MAX_AGE_HOURS", "26")
	now := time.Date(2026, 10, 7, 3, 0, 0, 0, time.UTC)
	artifact, checksum := writeCronBackupPair(t, directory, "sqlite", now.Add(-time.Hour), filepath.Base(filepath.Join(directory, "xirang-sqlite-20261007-020000.db")))
	artifactBefore, err := os.Lstat(artifact)
	if err != nil {
		t.Fatal(err)
	}
	checksumBefore, err := os.Lstat(checksum)
	if err != nil {
		t.Fatal(err)
	}
	got := cronBackupStatusRequest(t, NewSystemHandler(newCronBackupSQLiteDB(t)), now)
	if got.Status != cronBackupStatusFresh || got.ArtifactName != filepath.Base(artifact) {
		t.Fatalf("规范 basename 响应 = %+v", got)
	}
	artifactAfter, err := os.Lstat(artifact)
	if err != nil {
		t.Fatal(err)
	}
	checksumAfter, err := os.Lstat(checksum)
	if err != nil {
		t.Fatal(err)
	}
	for name, beforeAfter := range map[string][2]os.FileInfo{
		"artifact": {artifactBefore, artifactAfter},
		"checksum": {checksumBefore, checksumAfter},
	} {
		before, after := beforeAfter[0], beforeAfter[1]
		if !os.SameFile(before, after) || before.Mode() != after.Mode() || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
			t.Fatalf("%s 元数据发生变化: before=%+v after=%+v", name, before, after)
		}
	}

	if err := os.WriteFile(checksum, []byte(strings.Repeat("a", 64)+" *"+filepath.Join(directory, filepath.Base(artifact))+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(checksum, now.Add(-time.Hour), now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	got = cronBackupStatusRequest(t, NewSystemHandler(newCronBackupSQLiteDB(t)), now)
	if got.Status != cronBackupStatusFresh {
		t.Fatalf("规范绝对路径响应 = %+v", got)
	}
}

func TestCronBackupStatusUsesPostgresDialectNamespace(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN 未配置")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开 PostgreSQL 测试数据库失败: %v", err)
	}
	directory := t.TempDir()
	t.Setenv("DB_TYPE", "sqlite")
	t.Setenv("CRON_DB_BACKUP_DIR", directory)
	t.Setenv("CRON_DB_BACKUP_MAX_AGE_HOURS", "26")
	now := time.Date(2026, 10, 7, 3, 0, 0, 0, time.UTC)
	writeCronBackupPair(t, directory, "postgres", now.Add(-time.Hour), "")
	got := cronBackupStatusRequest(t, NewSystemHandler(db), now)
	if got.Engine != "postgres" || got.Status != cronBackupStatusFresh || !strings.HasSuffix(got.ArtifactName, ".dump") {
		t.Fatalf("PostgreSQL 产物命名空间响应 = %+v", got)
	}
}

func TestCronBackupStatusRejectsRootSymlinkSpecialAndPermissionDirectories(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("OpenRoot 特殊文件安全测试仅在 Unix 上运行")
	}
	now := time.Date(2026, 10, 7, 3, 0, 0, 0, time.UTC)

	t.Run("root symlink", func(t *testing.T) {
		parent := t.TempDir()
		outside := t.TempDir()
		rootLink := filepath.Join(parent, "backup-root")
		if err := os.Symlink(outside, rootLink); err != nil {
			t.Fatal(err)
		}
		t.Setenv("CRON_DB_BACKUP_DIR", rootLink)
		t.Setenv("CRON_DB_BACKUP_MAX_AGE_HOURS", "26")
		got := cronBackupStatusRequest(t, NewSystemHandler(newCronBackupSQLiteDB(t)), now)
		if got.Status != cronBackupStatusInvalidConfiguration {
			t.Fatalf("根目录符号链接状态 = %+v", got)
		}
	})

	t.Run("root special file", func(t *testing.T) {
		parent := t.TempDir()
		rootFIFO := filepath.Join(parent, "backup-root")
		if err := syscall.Mkfifo(rootFIFO, 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("CRON_DB_BACKUP_DIR", rootFIFO)
		t.Setenv("CRON_DB_BACKUP_MAX_AGE_HOURS", "26")
		got := cronBackupStatusRequest(t, NewSystemHandler(newCronBackupSQLiteDB(t)), now)
		if got.Status != cronBackupStatusDirectoryUnreadable {
			t.Fatalf("根目录特殊文件状态 = %+v", got)
		}
	})

	t.Run("special file", func(t *testing.T) {
		directory := t.TempDir()
		fifo := filepath.Join(directory, "xirang-sqlite-20261007-020000.db")
		if err := syscall.Mkfifo(fifo, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fifo+".sha256", []byte(strings.Repeat("a", 64)+"  "+fifo+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("CRON_DB_BACKUP_DIR", directory)
		t.Setenv("CRON_DB_BACKUP_MAX_AGE_HOURS", "26")
		got := cronBackupStatusRequest(t, NewSystemHandler(newCronBackupSQLiteDB(t)), now)
		if got.Status != cronBackupStatusNoCompleteBackup {
			t.Fatalf("特殊文件状态 = %+v", got)
		}
	})

	t.Run("permission denied", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root 用户可绕过目录权限")
		}
		directory := t.TempDir()
		if err := os.Chmod(directory, 0o000); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = os.Chmod(directory, 0o700) }()
		t.Setenv("CRON_DB_BACKUP_DIR", directory)
		t.Setenv("CRON_DB_BACKUP_MAX_AGE_HOURS", "26")
		got := cronBackupStatusRequest(t, NewSystemHandler(newCronBackupSQLiteDB(t)), now)
		if got.Status != cronBackupStatusDirectoryUnreadable {
			t.Fatalf("无权限目录状态 = %+v", got)
		}
	})
}

func TestCronBackupStatusNonblockingChangedRegularInfo(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("FIFO 替换回归仅在 Unix 上运行")
	}
	directory := t.TempDir()
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()

	artifactName := "xirang-sqlite-20261007-020000.db"
	artifactPath := filepath.Join(directory, artifactName)
	if err := os.WriteFile(artifactPath, []byte("FAKE_BACKUP_ARTIFACT_FOR_TEST_ONLY"), 0o600); err != nil {
		t.Fatal(err)
	}
	artifactInfo, err := root.Lstat(artifactName)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(artifactPath); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(artifactPath, 0o600); err != nil {
		t.Fatal(err)
	}
	if verifyCronBackupFile(root, artifactName, artifactInfo) {
		t.Fatal("regular 文件被 FIFO 替换后不应通过验证")
	}

	checksumName := artifactName + ".sha256"
	checksumPath := filepath.Join(directory, checksumName)
	if err := os.WriteFile(checksumPath, []byte(strings.Repeat("a", 64)+"  "+artifactName+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	checksumInfo, err := root.Lstat(checksumName)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(checksumPath); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(checksumPath, 0o600); err != nil {
		t.Fatal(err)
	}
	var buffer [cronBackupMaxChecksumBytes]byte
	if _, ok, limit := readCronChecksumRecord(root, checksumName, checksumInfo, buffer[:]); ok || limit {
		t.Fatalf("FIFO checksum 不应读取成功: ok=%v limit=%v", ok, limit)
	}
}
