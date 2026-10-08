package handlers

import (
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	cronBackupStatusNotConfigured        = "not_configured"
	cronBackupStatusInvalidConfiguration = "invalid_configuration"
	cronBackupStatusDirectoryUnreadable  = "directory_unreadable"
	cronBackupStatusNoCompleteBackup     = "no_complete_backup"
	cronBackupStatusScanLimitExceeded    = "scan_limit_exceeded"
	cronBackupStatusClockAnomaly         = "clock_anomaly"
	cronBackupStatusStale                = "stale"
	cronBackupStatusFresh                = "fresh"

	cronBackupEvidenceArtifactPair = "artifact_pair"
	cronBackupTimeSourceMtime      = "mtime"

	cronBackupDefaultMaxAgeHours = 26
	cronBackupMinMaxAgeHours     = 1
	cronBackupMaxMaxAgeHours     = 8760

	cronBackupMaxEntries       = 4096
	cronBackupMaxChecksumBytes = 4096
	cronBackupMaxReadBytes     = 4 * 1024 * 1024
	cronBackupReadDirBatch     = 256
)

// CronBackupStatusResponse is the bounded, read-only observation of the
// configured cron backup artifact pair. It intentionally does not claim that a
// scheduler ran, that the artifact contents are valid, or that a restore was
// exercised.
type CronBackupStatusResponse struct {
	Status string `json:"status" enums:"not_configured,invalid_configuration,directory_unreadable,no_complete_backup,scan_limit_exceeded,clock_anomaly,stale,fresh"`
	// Engine is empty when the runtime dialect is unsupported and Status is invalid_configuration.
	Engine           string `json:"engine"`
	CheckedAt        string `json:"checked_at" format:"date-time"`
	MaxAgeSeconds    int64  `json:"max_age_seconds"`
	Directory        string `json:"directory,omitempty"`
	LatestCompleteAt string `json:"latest_complete_at,omitempty" format:"date-time"`
	ArtifactName     string `json:"artifact_name,omitempty"`
	Evidence         string `json:"evidence" enums:"artifact_pair"`
	TimeSource       string `json:"time_source" enums:"mtime"`
	ContentVerified  bool   `json:"content_verified"`
}

type cronBackupScanResult struct {
	status       string
	latestMTime  time.Time
	artifactName string
	haveLatest   bool
}

// CronBackupStatus godoc
// @Summary      查询 cron 数据库备份产物状态
// @Description  只读检查配置目录中的受管数据库产物及其校验文件；不执行 cron、不读取整库且不验证内容摘要。
// @Tags         system
// @Security     Bearer
// @Produce      json
// @Success      200  {object} handlers.Response{data=handlers.CronBackupStatusResponse}
// @Failure      401  {object} handlers.Response
// @Failure      403  {object} handlers.Response
// @Router       /system/cron-backup-status [get]
func (h *SystemHandler) CronBackupStatus(c *gin.Context) {
	checkedAt := time.Now()
	var db *gorm.DB
	if h != nil {
		db = h.db
		if h.now != nil {
			checkedAt = h.now()
		}
	}
	checkedAt = checkedAt.UTC()

	engine := cronBackupRuntimeEngine(db)
	maxAgeSeconds, maxAgeOK := cronBackupMaxAgeSeconds()
	response := CronBackupStatusResponse{
		Status:          cronBackupStatusInvalidConfiguration,
		Engine:          engine,
		CheckedAt:       checkedAt.Format(time.RFC3339Nano),
		MaxAgeSeconds:   maxAgeSeconds,
		Evidence:        cronBackupEvidenceArtifactPair,
		TimeSource:      cronBackupTimeSourceMtime,
		ContentVerified: false,
	}

	if engine == "" || !maxAgeOK {
		respondOK(c, response)
		return
	}

	directory := strings.TrimSpace(os.Getenv("CRON_DB_BACKUP_DIR"))
	if directory == "" {
		response.Status = cronBackupStatusNotConfigured
		respondOK(c, response)
		return
	}

	absoluteDirectory, err := filepath.Abs(filepath.Clean(directory))
	if err != nil {
		respondOK(c, response)
		return
	}
	response.Directory = absoluteDirectory

	result := scanCronBackupDirectory(absoluteDirectory, engine, checkedAt, time.Duration(maxAgeSeconds)*time.Second)
	response.Status = result.status
	if result.haveLatest {
		response.LatestCompleteAt = result.latestMTime.UTC().Format(time.RFC3339Nano)
		response.ArtifactName = result.artifactName
	}
	respondOK(c, response)
}

func cronBackupRuntimeEngine(db *gorm.DB) string {
	if db == nil || db.Dialector == nil {
		return ""
	}
	name := strings.ToLower(strings.TrimSpace(db.Name()))
	switch name {
	case "sqlite", "postgres":
		return name
	default:
		return ""
	}
}

func cronBackupMaxAgeSeconds() (int64, bool) {
	raw := strings.TrimSpace(os.Getenv("CRON_DB_BACKUP_MAX_AGE_HOURS"))
	if raw == "" {
		return cronBackupDefaultMaxAgeHours * 60 * 60, true
	}
	hours, err := strconv.Atoi(raw)
	if err != nil || hours < cronBackupMinMaxAgeHours || hours > cronBackupMaxMaxAgeHours {
		return 0, false
	}
	return int64(hours) * 60 * 60, true
}

func scanCronBackupDirectory(directory, engine string, checkedAt time.Time, maxAge time.Duration) cronBackupScanResult {
	result := cronBackupScanResult{status: cronBackupStatusNoCompleteBackup}

	initialRootInfo, err := os.Lstat(directory)
	if err != nil {
		result.status = cronBackupStatusDirectoryUnreadable
		return result
	}
	if initialRootInfo.Mode()&os.ModeSymlink != 0 {
		result.status = cronBackupStatusInvalidConfiguration
		return result
	}
	if !initialRootInfo.IsDir() {
		result.status = cronBackupStatusDirectoryUnreadable
		return result
	}
	if !cronBackupDirectoryPreflight(directory) {
		result.status = cronBackupStatusDirectoryUnreadable
		return result
	}

	root, err := os.OpenRoot(directory)
	if err != nil {
		result.status = cronBackupStatusDirectoryUnreadable
		return result
	}
	defer func() { _ = root.Close() }()

	openedRootInfo, err := root.Stat(".")
	if err != nil {
		result.status = cronBackupStatusDirectoryUnreadable
		return result
	}
	currentRootInfo, err := os.Lstat(directory)
	if err != nil {
		result.status = cronBackupStatusDirectoryUnreadable
		return result
	}
	if !cronBackupRootIdentityMatches(initialRootInfo, openedRootInfo, currentRootInfo) {
		result.status = cronBackupStatusInvalidConfiguration
		return result
	}

	directoryFile, err := root.OpenFile(".", os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		result.status = cronBackupStatusDirectoryUnreadable
		return result
	}
	defer func() { _ = directoryFile.Close() }()

	candidates, scanStatus := enumerateCronBackupCandidates(directoryFile, engine)
	if scanStatus != "" {
		result.status = scanStatus
		return result
	}

	var checksumBuffer [cronBackupMaxChecksumBytes]byte
	var totalChecksumBytes int64
	for _, candidate := range candidates {
		artifactInfo, err := root.Lstat(candidate)
		if err != nil || !cronBackupRegularNonEmpty(artifactInfo) {
			continue
		}

		checksumName := candidate + ".sha256"
		checksumInfo, err := root.Lstat(checksumName)
		if err != nil || !cronBackupRegularNonEmpty(checksumInfo) {
			continue
		}
		if checksumInfo.Size() > cronBackupMaxChecksumBytes {
			result.status = cronBackupStatusScanLimitExceeded
			result.haveLatest = false
			result.artifactName = ""
			return result
		}
		if totalChecksumBytes+checksumInfo.Size() > cronBackupMaxReadBytes {
			result.status = cronBackupStatusScanLimitExceeded
			result.haveLatest = false
			result.artifactName = ""
			return result
		}
		totalChecksumBytes += checksumInfo.Size()

		if !verifyCronBackupFile(root, candidate, artifactInfo) {
			continue
		}
		checksumSize, checksumOK, limitExceeded := readCronChecksumRecord(root, checksumName, checksumInfo, checksumBuffer[:])
		if limitExceeded {
			result.status = cronBackupStatusScanLimitExceeded
			result.haveLatest = false
			result.artifactName = ""
			return result
		}
		if !checksumOK || !cronChecksumRecordMatches(checksumBuffer[:checksumSize], candidate, directory) {
			continue
		}
		artifactAfter, artifactOK := cronBackupPathInfo(root, candidate, artifactInfo)
		if !artifactOK {
			continue
		}
		checksumAfter, checksumOK := cronBackupPathInfo(root, checksumName, checksumInfo)
		if !checksumOK {
			continue
		}
		pairMTime := artifactAfter.ModTime()
		if checksumAfter.ModTime().After(pairMTime) {
			pairMTime = checksumAfter.ModTime()
		}
		if !result.haveLatest || pairMTime.After(result.latestMTime) {
			result.haveLatest = true
			result.latestMTime = pairMTime
			result.artifactName = candidate
		}
	}

	finalRootInfo, err := os.Lstat(directory)
	if err != nil {
		result.status = cronBackupStatusDirectoryUnreadable
		result.haveLatest = false
		result.artifactName = ""
		return result
	}
	if !cronBackupRootIdentityMatches(initialRootInfo, openedRootInfo, finalRootInfo) {
		result.status = cronBackupStatusInvalidConfiguration
		result.haveLatest = false
		result.artifactName = ""
		return result
	}

	if !result.haveLatest {
		result.status = cronBackupStatusNoCompleteBackup
		return result
	}
	if result.latestMTime.After(checkedAt) {
		result.status = cronBackupStatusClockAnomaly
		return result
	}
	if checkedAt.Sub(result.latestMTime) <= maxAge {
		result.status = cronBackupStatusFresh
	} else {
		result.status = cronBackupStatusStale
	}
	return result
}

func enumerateCronBackupCandidates(directoryFile *os.File, engine string) ([]string, string) {
	candidates := make([]string, 0, 16)
	entryCount := 0
	for {
		entries, err := directoryFile.ReadDir(cronBackupReadDirBatch)
		for _, entry := range entries {
			entryCount++
			if entryCount > cronBackupMaxEntries {
				return nil, cronBackupStatusScanLimitExceeded
			}
			if isManagedCronBackupFilename(entry.Name(), engine) {
				candidates = append(candidates, entry.Name())
			}
		}
		if err == io.EOF {
			return candidates, ""
		}
		if err != nil {
			return nil, cronBackupStatusDirectoryUnreadable
		}
	}
}

func isManagedCronBackupFilename(name, engine string) bool {
	prefix, suffix := "", ""
	switch engine {
	case "sqlite":
		prefix, suffix = "xirang-sqlite-", ".db"
	case "postgres":
		prefix, suffix = "xirang-postgres-", ".dump"
	default:
		return false
	}
	if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, suffix) {
		return false
	}
	stamp := strings.TrimSuffix(strings.TrimPrefix(name, prefix), suffix)
	if len(stamp) != len("20060102-150405") || stamp[8] != '-' {
		return false
	}
	for index, char := range stamp {
		if index == 8 {
			continue
		}
		if char < '0' || char > '9' {
			return false
		}
	}
	_, err := time.Parse("20060102-150405", stamp)
	return err == nil
}

func cronBackupDirectoryPreflight(directory string) bool {
	file, err := os.OpenFile(directory, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return false
	}
	info, statErr := file.Stat()
	closeErr := file.Close()
	return statErr == nil && closeErr == nil && info != nil && info.IsDir()
}

func cronBackupRootIdentityMatches(initial, opened, current os.FileInfo) bool {
	return initial != nil && opened != nil && current != nil &&
		initial.Mode()&os.ModeSymlink == 0 && opened.IsDir() && current.Mode()&os.ModeSymlink == 0 && current.IsDir() &&
		os.SameFile(initial, opened) && os.SameFile(opened, current)
}

func cronBackupRegularNonEmpty(info os.FileInfo) bool {
	return info != nil && info.Mode().IsRegular() && info.Size() > 0
}

func cronBackupFileInfoMatches(before, after os.FileInfo) bool {
	return cronBackupRegularNonEmpty(before) && cronBackupRegularNonEmpty(after) &&
		os.SameFile(before, after) && before.Size() == after.Size() && before.ModTime().Equal(after.ModTime())
}

func verifyCronBackupFile(root *os.Root, name string, initial os.FileInfo) bool {
	if !cronBackupRegularNonEmpty(initial) {
		return false
	}
	file, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return false
	}
	opened, statErr := file.Stat()
	if statErr != nil || !cronBackupFileInfoMatches(initial, opened) {
		_ = file.Close()
		return false
	}
	after, statErr := file.Stat()
	closeErr := file.Close()
	if statErr != nil || closeErr != nil || !cronBackupFileInfoMatches(initial, after) {
		return false
	}
	pathInfo, err := root.Lstat(name)
	return err == nil && cronBackupFileInfoMatches(initial, pathInfo)
}

func cronBackupPathInfo(root *os.Root, name string, initial os.FileInfo) (os.FileInfo, bool) {
	pathInfo, err := root.Lstat(name)
	if err != nil || !cronBackupFileInfoMatches(initial, pathInfo) {
		return nil, false
	}
	return pathInfo, true
}

func readCronChecksumRecord(root *os.Root, name string, initial os.FileInfo, buffer []byte) (int, bool, bool) {
	if !cronBackupRegularNonEmpty(initial) {
		return 0, false, false
	}
	if initial.Size() > cronBackupMaxChecksumBytes {
		return 0, false, true
	}
	size := int(initial.Size())
	if size > len(buffer) {
		return 0, false, true
	}
	file, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return 0, false, false
	}
	opened, statErr := file.Stat()
	if statErr != nil {
		_ = file.Close()
		return 0, false, false
	}
	if opened.Size() > cronBackupMaxChecksumBytes {
		_ = file.Close()
		return 0, false, true
	}
	if !cronBackupFileInfoMatches(initial, opened) {
		_ = file.Close()
		return 0, false, false
	}

	_, readErr := io.ReadFull(file, buffer[:size])
	after, afterErr := file.Stat()
	closeErr := file.Close()
	if readErr != nil || afterErr != nil || closeErr != nil {
		return 0, false, false
	}
	if after.Size() > cronBackupMaxChecksumBytes {
		return 0, false, true
	}
	if !cronBackupFileInfoMatches(initial, after) {
		return 0, false, false
	}
	pathInfo, pathErr := root.Lstat(name)
	if pathErr != nil {
		return 0, false, false
	}
	if pathInfo.Size() > cronBackupMaxChecksumBytes {
		return 0, false, true
	}
	if !cronBackupFileInfoMatches(initial, pathInfo) {
		return 0, false, false
	}
	return size, true, false
}

func cronChecksumRecordMatches(record []byte, artifactName, directory string) bool {
	if len(record) > 0 && record[len(record)-1] == '\n' {
		record = record[:len(record)-1]
	}
	if len(record) < 66 {
		return false
	}
	for _, char := range record {
		if char == '\n' || char == '\r' {
			return false
		}
	}
	for _, char := range record[:64] {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') && (char < 'A' || char > 'F') {
			return false
		}
	}
	if record[64] != ' ' || (record[65] != ' ' && record[65] != '*') {
		return false
	}
	path := string(record[66:])
	if path == artifactName {
		return true
	}
	canonicalPath := filepath.Join(directory, artifactName)
	return filepath.IsAbs(canonicalPath) && path == canonicalPath
}
