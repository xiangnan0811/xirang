package handlers

import (
	"errors"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type systemBackupSource string

const (
	systemBackupSourceWeb  systemBackupSource = "web"
	systemBackupSourceCron systemBackupSource = "cron"

	systemBackupReadDirBatch     = 256
	systemBackupMaxEntries       = 4096
	systemBackupMaxChecksumBytes = 4096
	systemBackupMaxChecksumTotal = 4 << 20
	systemBackupMaxDataBytes     = int64(4 << 30)
)

var (
	errSystemBackupRequestInvalid       = errors.New("invalid backup request")
	errSystemBackupSourceUnsupported    = errors.New("backup source unsupported")
	errSystemBackupSourceNotConfigured  = errors.New("backup source not configured")
	errSystemBackupDirectoryUnavailable = errors.New("backup directory unavailable")
	errSystemBackupNotFound             = errors.New("backup not found")
	errSystemBackupNotReady             = errors.New("backup not ready")
	errSystemBackupUnsafe               = errors.Join(errSystemBackupNotReady, errors.New("unsafe backup object"))
	errSystemBackupScanLimitExceeded    = errors.New("backup scan limit exceeded")
)

// SystemBackupEntry describes metadata only. Downloadable does not attest to
// content integrity or reserve the artifact against retention cleanup.
type SystemBackupEntry struct {
	Filename          string             `json:"filename"`
	Size              int64              `json:"size"`
	CreatedAt         string             `json:"created_at"`
	SHA256            string             `json:"sha256"`
	Source            systemBackupSource `json:"source"`
	Engine            string             `json:"engine"`
	Downloadable      bool               `json:"downloadable"`
	UnavailableReason string             `json:"unavailable_reason"`
	PackagedSize      int64              `json:"packaged_size"`
}

func resolveSystemBackupSource(db *gorm.DB, source systemBackupSource) (directory, engine string, err error) {
	engine = cronBackupRuntimeEngine(db)
	switch source {
	case systemBackupSourceWeb:
		if !isSQLiteRuntime() || engine != "sqlite" {
			return "", "", errSystemBackupSourceUnsupported
		}
		directory = os.Getenv("DB_BACKUP_DIR")
		if directory == "" {
			dbPath := os.Getenv("SQLITE_PATH")
			if dbPath == "" {
				dbPath = "./xirang.db"
			}
			directory = filepath.Join(filepath.Dir(dbPath), "backups")
		}
	case systemBackupSourceCron:
		directory = strings.TrimSpace(os.Getenv("CRON_DB_BACKUP_DIR"))
		if directory == "" {
			return "", "", errSystemBackupSourceNotConfigured
		}
		if engine == "" {
			return "", "", errSystemBackupSourceUnsupported
		}
	default:
		return "", "", errSystemBackupRequestInvalid
	}
	directory, err = filepath.Abs(directory)
	if err != nil {
		return "", "", errSystemBackupDirectoryUnavailable
	}
	return directory, engine, nil
}

// This stricter predicate is intentionally separate from Web retention's legacy
// isManagedBackupFilename predicate.
func isManagedSystemWebBackupFilename(name string) bool {
	if !strings.HasPrefix(name, "xirang-") || !strings.HasSuffix(name, ".db") {
		return false
	}
	stem := name[len("xirang-") : len(name)-len(".db")]
	if len(stem) < 15 || stem[8] != '-' {
		return false
	}
	for i, char := range stem[:15] {
		if i != 8 && (char < '0' || char > '9') {
			return false
		}
	}
	if _, err := time.Parse("20060102-150405", stem[:15]); err != nil {
		return false
	}
	if len(stem) == 15 {
		return true
	}
	if stem[15] != '-' || len(stem) < 17 || len(stem) > 80 {
		return false
	}
	for _, char := range stem[16:] {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '_' && char != '-' {
			return false
		}
	}
	return true
}

func isManagedSystemBackupFilename(source systemBackupSource, engine, name string) bool {
	switch source {
	case systemBackupSourceWeb:
		return engine == "sqlite" && isManagedSystemWebBackupFilename(name)
	case systemBackupSourceCron:
		return isManagedCronBackupFilename(name, engine)
	default:
		return false
	}
}

// Legacy cron labels are comparison-only. Never use a checksum label for access.
// The observer's parser remains unchanged and keeps its narrower contract.
func parseSystemBackupChecksum(record []byte, source systemBackupSource, name, directory string) (string, bool) {
	if len(record) == 0 || len(record) > systemBackupMaxChecksumBytes {
		return "", false
	}
	line := strings.TrimSuffix(string(record), "\n")
	if len(line) < 67 || strings.ContainsAny(line, "\x00\\\r\n") {
		return "", false
	}
	label := line[66:]
	if strings.HasSuffix(label, "/") {
		return "", false
	}
	if source != systemBackupSourceWeb && source != systemBackupSourceCron {
		return "", false
	}
	if !cronChecksumRecordMatches(record, name, directory) {
		if source != systemBackupSourceCron || path.Base(path.Clean(label)) != name {
			return "", false
		}
		// Reuse the observer's digest and single-record validation, substituting
		// only a label that has already been matched without opening it.
		if !cronChecksumRecordMatches([]byte(line[:66]+name), name, directory) {
			return "", false
		}
	}
	return strings.ToLower(line[:64]), true
}

type systemBackupRoot struct {
	root      *os.Root
	directory string
	initial   os.FileInfo
	opened    os.FileInfo
}

func openSystemBackupRoot(directory string) (*systemBackupRoot, error) {
	initial, err := os.Lstat(directory)
	if err != nil {
		return nil, errors.Join(errSystemBackupDirectoryUnavailable, err)
	}
	if initial.Mode()&os.ModeSymlink != 0 || !initial.IsDir() || !cronBackupDirectoryPreflight(directory) {
		return nil, errSystemBackupDirectoryUnavailable
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, errSystemBackupDirectoryUnavailable
	}
	opened, err := root.Stat(".")
	if err != nil {
		_ = root.Close()
		return nil, errSystemBackupDirectoryUnavailable
	}
	held := &systemBackupRoot{root: root, directory: directory, initial: initial, opened: opened}
	if err := held.validate(); err != nil {
		_ = root.Close()
		return nil, err
	}
	return held, nil
}

func (r *systemBackupRoot) validate() error {
	current, err := os.Lstat(r.directory)
	if err != nil || !cronBackupRootIdentityMatches(r.initial, r.opened, current) {
		return errSystemBackupDirectoryUnavailable
	}
	return nil
}

// openRegular pins a nonblocking, non-symlink regular descriptor and checks it
// against its initial rooted Lstat. It never follows an artifact's target.
func (r *systemBackupRoot) openRegular(name string) (*os.File, os.FileInfo, error) {
	initial, err := r.root.Lstat(name)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil, errSystemBackupNotFound
		}
		return nil, nil, errSystemBackupDirectoryUnavailable
	}
	if !initial.Mode().IsRegular() {
		return nil, nil, errSystemBackupUnsafe
	}
	if initial.Size() == 0 {
		return nil, nil, errSystemBackupNotReady
	}
	file, err := r.root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		switch {
		case errors.Is(err, os.ErrNotExist):
			return nil, nil, errSystemBackupNotFound
		case errors.Is(err, syscall.ELOOP):
			return nil, nil, errSystemBackupUnsafe
		default:
			return nil, nil, errSystemBackupDirectoryUnavailable
		}
	}
	opened, err := file.Stat()
	if err != nil || !cronBackupFileInfoMatches(initial, opened) {
		_ = file.Close()
		return nil, nil, errSystemBackupUnsafe
	}
	return file, opened, nil
}

func (r *systemBackupRoot) matchesPath(name string, initial os.FileInfo) bool {
	current, err := r.root.Lstat(name)
	return err == nil && cronBackupFileInfoMatches(initial, current)
}

type systemBackupScanBudget struct {
	entries       int
	checksumBytes int64
}

func (b *systemBackupScanBudget) reserveChecksum(size int64) error {
	if size > systemBackupMaxChecksumTotal-b.checksumBytes {
		return errSystemBackupScanLimitExceeded
	}
	b.checksumBytes += size
	return nil
}

// systemBackupFilePair owns File until Close. Info and SHA256 are the pinned
// metadata/record, not a content verification. After successful pin, callers
// must use only this descriptor and must not revalidate or reopen by pathname.
type systemBackupFilePair struct {
	File     *os.File
	Info     os.FileInfo
	SHA256   string
	Filename string
}

func (p *systemBackupFilePair) Close() error { return p.File.Close() }

func pinSystemBackupFilePair(directory string, source systemBackupSource, engine, name string) (*systemBackupFilePair, error) {
	if !isManagedSystemBackupFilename(source, engine, name) {
		return nil, errSystemBackupRequestInvalid
	}
	root, err := openSystemBackupRoot(directory)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, errSystemBackupNotFound
		}
		return nil, err
	}
	defer func() { _ = root.root.Close() }()
	return root.pinPair(source, engine, name, nil)
}

func (r *systemBackupRoot) pinPair(source systemBackupSource, engine, name string, budget *systemBackupScanBudget) (*systemBackupFilePair, error) {
	if !isManagedSystemBackupFilename(source, engine, name) {
		return nil, errSystemBackupRequestInvalid
	}
	data, info, err := r.openRegular(name)
	if err != nil {
		return nil, err
	}
	keep := false
	defer func() {
		if !keep {
			_ = data.Close()
		}
	}()
	checksum, checksumInfo, err := r.openRegular(name + ".sha256")
	if err != nil {
		if errors.Is(err, errSystemBackupNotFound) {
			return nil, errSystemBackupNotReady
		}
		return nil, err
	}
	defer func() { _ = checksum.Close() }()
	if checksumInfo.Size() > systemBackupMaxChecksumBytes {
		return nil, errSystemBackupNotReady
	}
	if budget != nil {
		if err := budget.reserveChecksum(checksumInfo.Size()); err != nil {
			return nil, err
		}
	}
	var buffer [systemBackupMaxChecksumBytes]byte
	record := buffer[:int(checksumInfo.Size())]
	if _, err := io.ReadFull(checksum, record); err != nil {
		return nil, errSystemBackupNotReady
	}
	afterChecksum, checksumErr := checksum.Stat()
	afterData, dataErr := data.Stat()
	if checksumErr != nil || dataErr != nil || !cronBackupFileInfoMatches(checksumInfo, afterChecksum) || !cronBackupFileInfoMatches(info, afterData) ||
		!r.matchesPath(name, info) || !r.matchesPath(name+".sha256", checksumInfo) {
		return nil, errSystemBackupUnsafe
	}
	if err := r.validate(); err != nil {
		return nil, err
	}
	digest, valid := parseSystemBackupChecksum(record, source, name, r.directory)
	if !valid {
		return nil, errSystemBackupNotReady
	}
	keep = true
	return &systemBackupFilePair{File: data, Info: info, SHA256: digest, Filename: name}, nil
}

func systemBackupPackagedSize(size int64) int64 {
	// Bound arithmetic even for a filesystem reporting a near-MaxInt64 size.
	if size <= 0 || size > int64(^uint64(0)>>1)-3071 {
		return 0
	}
	return 2560 + ((size+511)/512)*512
}

func listSystemBackupFiles(directory string, source systemBackupSource, engine string) ([]SystemBackupEntry, error) {
	backups := make([]SystemBackupEntry, 0)
	root, err := openSystemBackupRoot(directory)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return backups, nil
		}
		return nil, err
	}
	defer func() { _ = root.root.Close() }()
	directoryFile, err := root.root.OpenFile(".", os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, errSystemBackupDirectoryUnavailable
	}
	defer func() { _ = directoryFile.Close() }()
	budget := &systemBackupScanBudget{}
	for {
		entries, readErr := directoryFile.ReadDir(systemBackupReadDirBatch)
		for _, entry := range entries {
			budget.entries++
			if budget.entries > systemBackupMaxEntries {
				return nil, errSystemBackupScanLimitExceeded
			}
			name := entry.Name()
			managed := isManagedSystemBackupFilename(source, engine, name)
			if source == systemBackupSourceWeb {
				if !strings.HasSuffix(name, ".db") {
					continue
				}
			} else if !managed {
				continue
			}
			info, err := root.root.Lstat(name)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					continue
				}
				return nil, errSystemBackupDirectoryUnavailable
			}
			if !info.Mode().IsRegular() || (!managed && info.Size() == 0) {
				continue
			}
			item := SystemBackupEntry{
				Filename: name, Size: info.Size(), CreatedAt: info.ModTime().Format(time.RFC3339),
				Source: source, Engine: engine, UnavailableReason: "unmanaged",
			}
			if managed {
				item.UnavailableReason = "incomplete"
				item.PackagedSize = systemBackupPackagedSize(item.Size)
				pair, err := root.pinPair(source, engine, name, budget)
				switch {
				case err == nil:
					item.Size = pair.Info.Size()
					item.CreatedAt = pair.Info.ModTime().Format(time.RFC3339)
					item.SHA256 = pair.SHA256
					item.PackagedSize = systemBackupPackagedSize(item.Size)
					item.Downloadable = item.Size <= systemBackupMaxDataBytes && item.PackagedSize > 0
					item.UnavailableReason = ""
					if !item.Downloadable {
						item.UnavailableReason = "too_large"
					}
					if err := pair.Close(); err != nil {
						return nil, errSystemBackupDirectoryUnavailable
					}
				case errors.Is(err, errSystemBackupNotFound):
					continue
				case errors.Is(err, errSystemBackupUnsafe):
					item.UnavailableReason = "unsafe"
				case errors.Is(err, errSystemBackupNotReady):
					// Missing, empty and malformed sidecars remain visible.
				default:
					return nil, err
				}
			}
			backups = append(backups, item)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, errSystemBackupDirectoryUnavailable
		}
	}
	if err := root.validate(); err != nil {
		return nil, err
	}
	sort.Slice(backups, func(i, j int) bool { return backups[i].Filename > backups[j].Filename })
	return backups, nil
}

type systemBackupErrorData struct {
	ErrorCode string `json:"error_code"`
}

func respondSystemBackupError(c *gin.Context, err error) {
	status, code, message := http.StatusServiceUnavailable, "BACKUP_DIRECTORY_UNAVAILABLE", "备份目录暂不可用"
	switch {
	case errors.Is(err, errSystemBackupRequestInvalid):
		status, code, message = http.StatusBadRequest, "BACKUP_REQUEST_INVALID", "备份请求无效"
	case errors.Is(err, errSystemBackupSourceUnsupported):
		status, code, message = http.StatusNotImplemented, "BACKUP_SOURCE_UNSUPPORTED", "当前不支持此备份来源"
	case errors.Is(err, errSystemBackupSourceNotConfigured):
		status, code, message = http.StatusConflict, "BACKUP_SOURCE_NOT_CONFIGURED", "尚未配置此备份来源"
	case errors.Is(err, errSystemBackupScanLimitExceeded):
		code, message = "BACKUP_SCAN_LIMIT_EXCEEDED", "备份目录超出扫描限制"
	}
	c.JSON(status, Response{Code: status, Message: message, Data: systemBackupErrorData{ErrorCode: code}})
}
