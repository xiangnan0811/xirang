package handlers

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

const systemBackupTestWebName = "xirang-20261017-120000-test.db"

func writeSystemBackupTestPair(t *testing.T, directory, name, label string) (string, []byte) {
	t.Helper()
	data := []byte("FAKE_DATABASE_BACKUP_FOR_TEST_ONLY")
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	if label == "" {
		label = name
	}
	if err := os.WriteFile(filepath.Join(directory, name), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, name+".sha256"), []byte(strings.ToUpper(digest)+"  "+label+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return digest, data
}

func systemBackupListRequest(t *testing.T, db *gorm.DB, query string) (int, []SystemBackupEntry, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/v1/system/backups"+query, nil)
	NewSystemHandler(db).ListBackups(ctx)
	var envelope struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Code != recorder.Code {
		t.Fatalf("HTTP/envelope mismatch: %d, %s", recorder.Code, recorder.Body.String())
	}
	if recorder.Code != http.StatusOK {
		var detail systemBackupErrorData
		if err := json.Unmarshal(envelope.Data, &detail); err != nil {
			t.Fatal(err)
		}
		if detail.ErrorCode == "" || strings.Contains(envelope.Message, string(os.PathSeparator)) {
			t.Fatalf("unsafe or unclassified error: %s", recorder.Body.String())
		}
		return recorder.Code, nil, detail.ErrorCode
	}
	var entries []SystemBackupEntry
	if err := json.Unmarshal(envelope.Data, &entries); err != nil {
		t.Fatal(err)
	}
	if entries == nil {
		t.Fatalf("successful list must be an array: %s", recorder.Body.String())
	}
	return recorder.Code, entries, ""
}

func TestSystemBackupSourceResolution(t *testing.T) {
	db := openSystemHandlerTestDB(t)
	t.Setenv("DB_TYPE", "")
	t.Setenv("DB_BACKUP_DIR", "")
	t.Setenv("SQLITE_PATH", "")
	want, err := filepath.Abs("backups")
	if err != nil {
		t.Fatal(err)
	}
	directory, engine, err := resolveSystemBackupSource(db, systemBackupSourceWeb)
	if err != nil || directory != want || engine != "sqlite" {
		t.Fatalf("default Web source = %q %q %v", directory, engine, err)
	}
	dbPath := filepath.Join(t.TempDir(), "data", "main.db")
	t.Setenv("SQLITE_PATH", dbPath)
	directory, _, err = resolveSystemBackupSource(db, systemBackupSourceWeb)
	if err != nil || directory != filepath.Join(filepath.Dir(dbPath), "backups") {
		t.Fatalf("SQLite-relative root = %q %v", directory, err)
	}
	t.Setenv("DB_BACKUP_DIR", "./custom/../backups")
	directory, _, err = resolveSystemBackupSource(db, systemBackupSourceWeb)
	if err != nil || directory != want {
		t.Fatalf("canonical explicit root = %q %v", directory, err)
	}
	t.Setenv("DB_TYPE", "postgres")
	if _, _, err := resolveSystemBackupSource(db, systemBackupSourceWeb); !errors.Is(err, errSystemBackupSourceUnsupported) {
		t.Fatalf("runtime gate = %v", err)
	}
	t.Setenv("CRON_DB_BACKUP_DIR", "  ./cron/../backups  ")
	directory, engine, err = resolveSystemBackupSource(db, systemBackupSourceCron)
	if err != nil || directory != want || engine != "sqlite" {
		t.Fatalf("cron must follow actual dialect, not DB_TYPE: %q %q %v", directory, engine, err)
	}
	t.Setenv("CRON_DB_BACKUP_DIR", " \t ")
	if _, _, err := resolveSystemBackupSource(db, systemBackupSourceCron); !errors.Is(err, errSystemBackupSourceNotConfigured) {
		t.Fatalf("empty cron root = %v", err)
	}
	t.Setenv("CRON_DB_BACKUP_DIR", t.TempDir())
	if _, _, err := resolveSystemBackupSource(nil, systemBackupSourceCron); !errors.Is(err, errSystemBackupSourceUnsupported) {
		t.Fatalf("unknown dialect = %v", err)
	}
	if _, _, err := resolveSystemBackupSource(db, "other"); !errors.Is(err, errSystemBackupRequestInvalid) {
		t.Fatalf("unknown source = %v", err)
	}
}

func TestSystemBackupPostgresDialectNamespace(t *testing.T) {
	// No SQL is needed to list files; use the real PostgreSQL dialector without
	// requiring a live server. Restore acceptance uses an isolated live server.
	db, err := gorm.Open(postgres.Open("host=127.0.0.1 port=1 user=FAKE_USER_FOR_TEST_ONLY dbname=FAKE_DATABASE_FOR_TEST_ONLY sslmode=disable"), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	t.Setenv("CRON_DB_BACKUP_DIR", directory)
	t.Setenv("DB_TYPE", "sqlite")
	resolved, engine, err := resolveSystemBackupSource(db, systemBackupSourceCron)
	if err != nil || resolved != directory || engine != "postgres" {
		t.Fatalf("PostgreSQL source = %q %q %v", resolved, engine, err)
	}
	if _, _, err := resolveSystemBackupSource(db, systemBackupSourceWeb); !errors.Is(err, errSystemBackupSourceUnsupported) {
		t.Fatalf("Web requires actual SQLite dialect: %v", err)
	}
	name := "xirang-postgres-20261017-120000.dump"
	digest, _ := writeSystemBackupTestPair(t, directory, name, "./old/mount/../backups/"+name)
	writeSystemBackupTestPair(t, directory, "xirang-sqlite-20261017-120000.db", "")
	writeSystemBackupTestPair(t, directory, systemBackupTestWebName, "")
	status, entries, code := systemBackupListRequest(t, db, "?source=cron")
	if status != http.StatusOK || code != "" || len(entries) != 1 || entries[0].Filename != name || entries[0].Engine != "postgres" || entries[0].SHA256 != digest || !entries[0].Downloadable {
		t.Fatalf("PostgreSQL namespace = %d %+v %s", status, entries, code)
	}
	pair, err := pinSystemBackupFilePair(directory, systemBackupSourceCron, "postgres", name)
	if err != nil {
		t.Fatal(err)
	}
	if err := pair.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSystemBackupStrictWebNames(t *testing.T) {
	for _, name := range []string{
		"xirang-20261017-120000.db", systemBackupTestWebName,
		"xirang-20261017-120000-" + strings.Repeat("a", 64) + ".db",
		"xirang-20261017-120000-A_z-9.db",
	} {
		if !isManagedSystemWebBackupFilename(name) {
			t.Errorf("valid managed name rejected: %q", name)
		}
	}
	for _, name := range []string{
		"xirang-20260230-120000.db", "xirang-20261017-250000.db", "xirang-20261017-120000-.db",
		"xirang-20261017-120000-" + strings.Repeat("a", 65) + ".db", "xirang-20261017-120000-a.b.db",
		"xirang-20261017-120000-a/b.db", "xirang-20261017-120000-a\\b.db", "xirang-20261017-120000-%61.db",
		"xirang-20261017-120000-ä.db", "xirang-20261017-120000-a\x00.db", "xirang-sqlite-20261017-120000.db",
		"../" + systemBackupTestWebName,
	} {
		if isManagedSystemWebBackupFilename(name) {
			t.Errorf("invalid managed name accepted: %q", name)
		}
	}
}

func TestSystemBackupChecksumLabels(t *testing.T) {
	directory := t.TempDir()
	name := "xirang-sqlite-20261017-120000.db"
	sum := sha256.Sum256([]byte("FAKE_CHECKSUM_INPUT_FOR_TEST_ONLY"))
	digest := hex.EncodeToString(sum[:])
	for _, tc := range []struct {
		label string
		web   bool
		cron  bool
	}{
		{name, true, true}, {filepath.Join(directory, name), true, true},
		{"backups/" + name, false, true}, {"./backups/../backups/" + name, false, true},
		{"/different/container/mount/" + name, false, true},
		{name + "/", false, false}, {"../wrong.db", false, false}, {"", false, false},
		{"dir\\" + name, false, false}, {"bad\x00/" + name, false, false},
		{"other\n" + name, false, false}, {"other\r" + name, false, false},
	} {
		for _, source := range []systemBackupSource{systemBackupSourceWeb, systemBackupSourceCron} {
			for _, separator := range []string{"  ", " *"} {
				record := []byte(strings.ToUpper(digest) + separator + tc.label + "\n")
				got, valid := parseSystemBackupChecksum(record, source, name, directory)
				want := tc.web
				if source == systemBackupSourceCron {
					want = tc.cron
				}
				if valid != want || valid && got != digest {
					t.Errorf("%s label %q: digest=%q valid=%v want=%v", source, tc.label, got, valid, want)
				}
			}
		}
	}
	for _, record := range []string{
		digest + " " + name, strings.Repeat("z", 64) + "  " + name,
		digest + "  " + name + "\n" + digest + "  " + name + "\n",
		digest + "  " + name + "\n\n", digest + "  " + name + "\r\n",
	} {
		if _, valid := parseSystemBackupChecksum([]byte(record), systemBackupSourceCron, name, directory); valid {
			t.Errorf("invalid checksum accepted: %q", record)
		}
	}
	legacy := []byte(digest + "  /old/mount/" + name + "\n")
	if cronChecksumRecordMatches(legacy, name, directory) {
		t.Fatal("observer unexpectedly accepts a cross-mount label")
	}
}

func TestSystemBackupPinRetainsOriginalDescriptorAfterUnlink(t *testing.T) {
	directory := t.TempDir()
	digest, original := writeSystemBackupTestPair(t, directory, systemBackupTestWebName, "")
	pair, err := pinSystemBackupFilePair(directory, systemBackupSourceWeb, "sqlite", systemBackupTestWebName)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pair.Close() }()
	if pair.SHA256 != digest || pair.Filename != systemBackupTestWebName || pair.Info.Size() != int64(len(original)) {
		t.Fatalf("pinned metadata = %+v", pair)
	}
	for _, name := range []string{systemBackupTestWebName, systemBackupTestWebName + ".sha256"} {
		if err := os.Remove(filepath.Join(directory, name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(directory, systemBackupTestWebName), []byte("FAKE_REPLACEMENT_FOR_TEST_ONLY"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, systemBackupTestWebName+".sha256"), []byte("invalid replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(pair.File)
	if err != nil || string(got) != string(original) || pair.SHA256 != digest {
		t.Fatalf("read changed after unlink/replacement: %q %v", got, err)
	}
	info, err := pair.File.Stat()
	if err != nil || !cronBackupFileInfoMatches(pair.Info, info) {
		t.Fatalf("unlinked original descriptor changed: %v", err)
	}
	if _, err := pair.File.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	got, err = io.ReadAll(pair.File)
	if err != nil || string(got) != string(original) {
		t.Fatalf("second pass did not read original: %q %v", got, err)
	}
}

func TestSystemBackupPinRejectsUnsafeAndIncompletePairs(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*testing.T, string)
		want   error
	}{
		{"missing data", func(t *testing.T, directory string) {
			removeSystemBackupTestFile(t, filepath.Join(directory, systemBackupTestWebName))
		}, errSystemBackupNotFound},
		{"missing checksum", func(t *testing.T, directory string) {
			removeSystemBackupTestFile(t, filepath.Join(directory, systemBackupTestWebName+".sha256"))
		}, errSystemBackupNotReady},
		{"empty data", func(t *testing.T, directory string) {
			writeSystemBackupTestFile(t, filepath.Join(directory, systemBackupTestWebName), nil)
		}, errSystemBackupNotReady},
		{"empty checksum", func(t *testing.T, directory string) {
			writeSystemBackupTestFile(t, filepath.Join(directory, systemBackupTestWebName+".sha256"), nil)
		}, errSystemBackupNotReady},
		{"invalid checksum", func(t *testing.T, directory string) {
			writeSystemBackupTestFile(t, filepath.Join(directory, systemBackupTestWebName+".sha256"), []byte("not a checksum"))
		}, errSystemBackupNotReady},
		{"oversize checksum", func(t *testing.T, directory string) {
			writeSystemBackupTestFile(t, filepath.Join(directory, systemBackupTestWebName+".sha256"), []byte(strings.Repeat("x", 4097)))
		}, errSystemBackupNotReady},
		{"data symlink", func(t *testing.T, directory string) {
			replaceSystemBackupTestSpecial(t, directory, systemBackupTestWebName, "symlink")
		}, errSystemBackupUnsafe},
		{"checksum symlink", func(t *testing.T, directory string) {
			replaceSystemBackupTestSpecial(t, directory, systemBackupTestWebName+".sha256", "symlink")
		}, errSystemBackupUnsafe},
		{"data FIFO", func(t *testing.T, directory string) {
			replaceSystemBackupTestSpecial(t, directory, systemBackupTestWebName, "fifo")
		}, errSystemBackupUnsafe},
		{"checksum FIFO", func(t *testing.T, directory string) {
			replaceSystemBackupTestSpecial(t, directory, systemBackupTestWebName+".sha256", "fifo")
		}, errSystemBackupUnsafe},
		{"data directory", func(t *testing.T, directory string) {
			replaceSystemBackupTestSpecial(t, directory, systemBackupTestWebName, "directory")
		}, errSystemBackupUnsafe},
	} {
		t.Run(tc.name, func(t *testing.T) {
			directory := t.TempDir()
			writeSystemBackupTestPair(t, directory, systemBackupTestWebName, "")
			tc.mutate(t, directory)
			pair, err := pinSystemBackupFilePair(directory, systemBackupSourceWeb, "sqlite", systemBackupTestWebName)
			if pair != nil {
				_ = pair.Close()
				t.Fatal("unsafe pair returned a data descriptor")
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("pin error = %v, want %v", err, tc.want)
			}
		})
	}
	if _, err := pinSystemBackupFilePair(filepath.Join(t.TempDir(), "missing"), systemBackupSourceWeb, "sqlite", systemBackupTestWebName); !errors.Is(err, errSystemBackupNotFound) {
		t.Fatalf("missing pin root = %v", err)
	}
	if _, err := pinSystemBackupFilePair(t.TempDir(), systemBackupSourceWeb, "sqlite", "unmanaged.db"); !errors.Is(err, errSystemBackupRequestInvalid) {
		t.Fatalf("unmanaged pin = %v", err)
	}
}

func removeSystemBackupTestFile(t *testing.T, name string) {
	t.Helper()
	if err := os.Remove(name); err != nil {
		t.Fatal(err)
	}
}

func writeSystemBackupTestFile(t *testing.T, name string, data []byte) {
	t.Helper()
	if err := os.WriteFile(name, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func replaceSystemBackupTestSpecial(t *testing.T, directory, name, kind string) {
	t.Helper()
	fullName := filepath.Join(directory, name)
	removeSystemBackupTestFile(t, fullName)
	var err error
	switch kind {
	case "symlink":
		target := filepath.Join(t.TempDir(), "outside")
		writeSystemBackupTestFile(t, target, []byte("FAKE_OUTSIDE_TARGET_FOR_TEST_ONLY"))
		err = os.Symlink(target, fullName)
	case "fifo":
		err = syscall.Mkfifo(fullName, 0600)
	case "directory":
		err = os.Mkdir(fullName, 0700)
	}
	if err != nil {
		t.Fatal(err)
	}
}

func TestSystemBackupRootIdentityAndPrePinDisappearance(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "backups")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	writeSystemBackupTestPair(t, directory, systemBackupTestWebName, "")
	root, err := openSystemBackupRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.root.Close() }()
	if err := os.Rename(directory, directory+"-original"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	writeSystemBackupTestPair(t, directory, systemBackupTestWebName, "")
	if err := root.validate(); !errors.Is(err, errSystemBackupDirectoryUnavailable) {
		t.Fatalf("replaced root was not rejected: %v", err)
	}
	if pair, err := root.pinPair(systemBackupSourceWeb, "sqlite", systemBackupTestWebName, nil); pair != nil || !errors.Is(err, errSystemBackupDirectoryUnavailable) {
		if pair != nil {
			_ = pair.Close()
		}
		t.Fatalf("pair pinned through replaced root: %v", err)
	}
	link := filepath.Join(t.TempDir(), "root-link")
	if err := os.Symlink(directory, link); err != nil {
		t.Fatal(err)
	}
	if held, err := openSystemBackupRoot(link); held != nil || !errors.Is(err, errSystemBackupDirectoryUnavailable) {
		t.Fatalf("symlink root accepted: %v", err)
	}
	removeSystemBackupTestFile(t, filepath.Join(directory, systemBackupTestWebName))
	if _, err := pinSystemBackupFilePair(directory, systemBackupSourceWeb, "sqlite", systemBackupTestWebName); !errors.Is(err, errSystemBackupNotFound) {
		t.Fatalf("pre-pin disappearance = %v", err)
	}
}

func TestSystemBackupOpenedObjectIdentityBarriers(t *testing.T) {
	// Execute at the precise boundary between descriptor acquisition and final
	// path identity validation. No sleep or probabilistic goroutine race.
	for _, member := range []string{systemBackupTestWebName, systemBackupTestWebName + ".sha256"} {
		for _, replacement := range []string{"regular", "symlink", "fifo"} {
			t.Run(member+"/"+replacement, func(t *testing.T) {
				directory := t.TempDir()
				writeSystemBackupTestPair(t, directory, systemBackupTestWebName, "")
				root, err := openSystemBackupRoot(directory)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = root.root.Close() }()
				file, info, err := root.openRegular(member)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = file.Close() }()
				if replacement == "regular" {
					removeSystemBackupTestFile(t, filepath.Join(directory, member))
					writeSystemBackupTestFile(t, filepath.Join(directory, member), []byte("FAKE_NEW_INODE_FOR_TEST_ONLY"))
				} else {
					replaceSystemBackupTestSpecial(t, directory, member, replacement)
				}
				if root.matchesPath(member, info) {
					t.Fatal("path replacement still matches pinned inode")
				}
				if _, err := file.Stat(); err != nil {
					t.Fatalf("original descriptor no longer available: %v", err)
				}
			})
		}
	}
	for _, mutation := range []string{"rewrite", "growth", "shorten"} {
		t.Run(mutation, func(t *testing.T) {
			directory := t.TempDir()
			_, original := writeSystemBackupTestPair(t, directory, systemBackupTestWebName, "")
			pair, err := pinSystemBackupFilePair(directory, systemBackupSourceWeb, "sqlite", systemBackupTestWebName)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = pair.Close() }()
			data := []byte(strings.Repeat("x", len(original)))
			switch mutation {
			case "growth":
				data = append(data, 'x')
			case "shorten":
				data = data[:len(data)-1]
			}
			fullName := filepath.Join(directory, systemBackupTestWebName)
			writeSystemBackupTestFile(t, fullName, data)
			changed := pair.Info.ModTime().Add(time.Second)
			if err := os.Chtimes(fullName, changed, changed); err != nil {
				t.Fatal(err)
			}
			after, err := pair.File.Stat()
			if err != nil || cronBackupFileInfoMatches(pair.Info, after) {
				t.Fatalf("same-inode mutation not detectable: %v", err)
			}
		})
	}
}

func TestSystemBackupListNamespacesAndAvailability(t *testing.T) {
	db := openSystemHandlerTestDB(t)
	directory := t.TempDir()
	t.Setenv("DB_TYPE", "sqlite")
	t.Setenv("DB_BACKUP_DIR", directory)
	t.Setenv("CRON_DB_BACKUP_DIR", directory)
	digest, data := writeSystemBackupTestPair(t, directory, systemBackupTestWebName, "")
	at := time.Date(2026, 10, 16, 5, 4, 3, 0, time.UTC)
	if err := os.Chtimes(filepath.Join(directory, systemBackupTestWebName), at, at); err != nil {
		t.Fatal(err)
	}
	cronName := "xirang-sqlite-20261017-120000.db"
	writeSystemBackupTestPair(t, directory, cronName, "./old//mount/../mount/"+cronName)
	writeSystemBackupTestPair(t, directory, "xirang-postgres-20261017-120000.dump", "")
	writeSystemBackupTestFile(t, filepath.Join(directory, "legacy.db"), data)
	writeSystemBackupTestFile(t, filepath.Join(directory, "empty-unmanaged.db"), nil)
	incomplete := "xirang-20261017-110000.db"
	writeSystemBackupTestFile(t, filepath.Join(directory, incomplete), data)
	empty := "xirang-20261017-100000.db"
	writeSystemBackupTestFile(t, filepath.Join(directory, empty), nil)
	unsafe := "xirang-20261017-090000.db"
	writeSystemBackupTestPair(t, directory, unsafe, "")
	replaceSystemBackupTestSpecial(t, directory, unsafe+".sha256", "symlink")
	for _, special := range []string{"symlink", "fifo", "directory"} {
		name := "xirang-20261017-080000-" + special + ".db"
		writeSystemBackupTestFile(t, filepath.Join(directory, name), data)
		replaceSystemBackupTestSpecial(t, directory, name, special)
	}
	status, entries, code := systemBackupListRequest(t, db, "")
	if status != http.StatusOK || code != "" || len(entries) != 6 {
		t.Fatalf("Web list = %d %+v %s", status, entries, code)
	}
	byName := make(map[string]SystemBackupEntry)
	for i, entry := range entries {
		if i > 0 && entries[i-1].Filename <= entry.Filename {
			t.Fatal("entries not in descending filename order")
		}
		if entry.Source != systemBackupSourceWeb || entry.Engine != "sqlite" {
			t.Fatalf("incorrect source metadata: %+v", entry)
		}
		byName[entry.Filename] = entry
	}
	valid := byName[systemBackupTestWebName]
	listedTime, err := time.Parse(time.RFC3339, valid.CreatedAt)
	if err != nil || !listedTime.Equal(at) || !valid.Downloadable || valid.SHA256 != digest || valid.PackagedSize != 3072 || valid.Size != int64(len(data)) {
		t.Fatalf("managed metadata = %+v", valid)
	}
	for name, reason := range map[string]string{"legacy.db": "unmanaged", cronName: "unmanaged", incomplete: "incomplete", empty: "incomplete", unsafe: "unsafe"} {
		if entry, ok := byName[name]; !ok || entry.UnavailableReason != reason || entry.Downloadable {
			t.Errorf("%s availability = %+v, want %s", name, entry, reason)
		}
	}
	status, entries, code = systemBackupListRequest(t, db, "?source=cron")
	if status != http.StatusOK || code != "" || len(entries) != 1 || entries[0].Filename != cronName || !entries[0].Downloadable {
		t.Fatalf("cron namespace = %d %+v %s", status, entries, code)
	}
	// A syntactically valid, wrong digest remains metadata-only downloadable;
	// the download handler, not listing, must hash and reject its content.
	writeSystemBackupTestFile(t, filepath.Join(directory, cronName+".sha256"), []byte(strings.Repeat("0", 64)+"  "+cronName+"\n"))
	_, entries, _ = systemBackupListRequest(t, db, "?source=cron")
	if len(entries) != 1 || !entries[0].Downloadable || entries[0].SHA256 != strings.Repeat("0", 64) {
		t.Fatalf("listing improperly treated metadata as a content check: %+v", entries)
	}
}

func TestSystemBackupListErrorsAndEmptySources(t *testing.T) {
	db := openSystemHandlerTestDB(t)
	t.Setenv("DB_TYPE", "sqlite")
	t.Setenv("DB_BACKUP_DIR", filepath.Join(t.TempDir(), "missing"))
	t.Setenv("CRON_DB_BACKUP_DIR", "")
	for _, query := range []string{"?source=", "?source=other", "?source=web&source=web", "?source=web&source=cron", "?source=%zz"} {
		status, _, code := systemBackupListRequest(t, db, query)
		if status != http.StatusBadRequest || code != "BACKUP_REQUEST_INVALID" {
			t.Errorf("query %q = %d %s", query, status, code)
		}
	}
	status, entries, _ := systemBackupListRequest(t, db, "")
	if status != http.StatusOK || len(entries) != 0 {
		t.Fatalf("missing configured Web directory = %d %+v", status, entries)
	}
	status, _, code := systemBackupListRequest(t, db, "?source=cron")
	if status != http.StatusConflict || code != "BACKUP_SOURCE_NOT_CONFIGURED" {
		t.Fatalf("unconfigured cron = %d %s", status, code)
	}
	t.Setenv("CRON_DB_BACKUP_DIR", filepath.Join(t.TempDir(), "missing"))
	status, entries, _ = systemBackupListRequest(t, db, "?source=cron")
	if status != http.StatusOK || len(entries) != 0 {
		t.Fatalf("missing configured cron directory = %d %+v", status, entries)
	}
	for _, kind := range []string{"file", "symlink", "fifo"} {
		t.Run(kind, func(t *testing.T) {
			name := filepath.Join(t.TempDir(), "root")
			writeSystemBackupTestFile(t, name, []byte("FAKE_NOT_DIRECTORY_FOR_TEST_ONLY"))
			if kind != "file" {
				replaceSystemBackupTestSpecial(t, filepath.Dir(name), filepath.Base(name), kind)
			}
			t.Setenv("DB_BACKUP_DIR", name)
			status, _, code := systemBackupListRequest(t, db, "")
			if status != http.StatusServiceUnavailable || code != "BACKUP_DIRECTORY_UNAVAILABLE" {
				t.Fatalf("unsafe directory = %d %s", status, code)
			}
		})
	}
	t.Setenv("DB_TYPE", "postgres")
	status, _, code = systemBackupListRequest(t, db, "")
	if status != http.StatusNotImplemented || code != "BACKUP_SOURCE_UNSUPPORTED" {
		t.Fatalf("unsupported Web = %d %s", status, code)
	}
}

func TestSystemBackupListRawEntryBudget(t *testing.T) {
	for _, source := range []systemBackupSource{systemBackupSourceWeb, systemBackupSourceCron} {
		t.Run(string(source), func(t *testing.T) {
			directory := t.TempDir()
			name := systemBackupTestWebName
			if source == systemBackupSourceCron {
				name = "xirang-sqlite-20261017-120000.db"
			}
			writeSystemBackupTestPair(t, directory, name, "")
			for i := range systemBackupMaxEntries - 2 {
				writeSystemBackupTestFile(t, filepath.Join(directory, fmt.Sprintf("unrelated-%04d", i)), nil)
			}
			entries, err := listSystemBackupFiles(directory, source, "sqlite")
			if err != nil || len(entries) != 1 {
				t.Fatalf("exact raw entry budget = %+v %v", entries, err)
			}
			writeSystemBackupTestFile(t, filepath.Join(directory, "one-too-many"), nil)
			entries, err = listSystemBackupFiles(directory, source, "sqlite")
			if !errors.Is(err, errSystemBackupScanLimitExceeded) || entries != nil {
				t.Fatalf("entry exhaustion returned partial success: %+v %v", entries, err)
			}
			t.Setenv("DB_TYPE", "sqlite")
			t.Setenv("DB_BACKUP_DIR", directory)
			t.Setenv("CRON_DB_BACKUP_DIR", directory)
			status, _, code := systemBackupListRequest(t, openSystemHandlerTestDB(t), "?source="+string(source))
			if status != http.StatusServiceUnavailable || code != "BACKUP_SCAN_LIMIT_EXCEEDED" {
				t.Fatalf("entry exhaustion response = %d %s", status, code)
			}
		})
	}
}

func TestSystemBackupChecksumReadBudgetsIncludeMalformedRecords(t *testing.T) {
	directory := t.TempDir()
	writeSystemBackupTestPair(t, directory, systemBackupTestWebName, "")
	root, err := openSystemBackupRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.root.Close() }()
	checksumName := filepath.Join(directory, systemBackupTestWebName+".sha256")
	writeSystemBackupTestFile(t, checksumName, []byte(strings.Repeat("x", systemBackupMaxChecksumBytes)))
	budget := &systemBackupScanBudget{checksumBytes: systemBackupMaxChecksumTotal - systemBackupMaxChecksumBytes}
	if pair, err := root.pinPair(systemBackupSourceWeb, "sqlite", systemBackupTestWebName, budget); pair != nil || !errors.Is(err, errSystemBackupNotReady) || budget.checksumBytes != systemBackupMaxChecksumTotal {
		t.Fatalf("malformed record not charged at boundary: %+v %v budget=%d", pair, err, budget.checksumBytes)
	}
	if pair, err := root.pinPair(systemBackupSourceWeb, "sqlite", systemBackupTestWebName, budget); pair != nil || !errors.Is(err, errSystemBackupScanLimitExceeded) || budget.checksumBytes != systemBackupMaxChecksumTotal {
		t.Fatalf("read allowed after checksum exhaustion: %+v %v budget=%d", pair, err, budget.checksumBytes)
	}
	writeSystemBackupTestFile(t, checksumName, []byte(strings.Repeat("x", systemBackupMaxChecksumBytes+1)))
	budget = &systemBackupScanBudget{}
	if pair, err := root.pinPair(systemBackupSourceWeb, "sqlite", systemBackupTestWebName, budget); pair != nil || !errors.Is(err, errSystemBackupNotReady) || budget.checksumBytes != 0 {
		t.Fatalf("oversize sidecar should not be read: %+v %v budget=%d", pair, err, budget.checksumBytes)
	}
}

func TestSystemBackupListChecksumBudgetDoesNotReturnPartialList(t *testing.T) {
	for _, source := range []systemBackupSource{systemBackupSourceWeb, systemBackupSourceCron} {
		t.Run(string(source), func(t *testing.T) {
			directory := t.TempDir()
			start := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
			prefix := "xirang-"
			if source == systemBackupSourceCron {
				prefix = "xirang-sqlite-"
			}
			for i := range systemBackupMaxChecksumTotal/systemBackupMaxChecksumBytes + 1 {
				name := prefix + start.Add(time.Duration(i)*time.Second).Format("20060102-150405") + ".db"
				writeSystemBackupTestFile(t, filepath.Join(directory, name), []byte("FAKE_BACKUP_FOR_TEST_ONLY"))
				writeSystemBackupTestFile(t, filepath.Join(directory, name+".sha256"), []byte(strings.Repeat("x", systemBackupMaxChecksumBytes)))
			}
			entries, err := listSystemBackupFiles(directory, source, "sqlite")
			if !errors.Is(err, errSystemBackupScanLimitExceeded) || entries != nil {
				t.Fatalf("checksum exhaustion returned partial success: %+v %v", entries, err)
			}
			t.Setenv("DB_TYPE", "sqlite")
			t.Setenv("DB_BACKUP_DIR", directory)
			t.Setenv("CRON_DB_BACKUP_DIR", directory)
			status, _, code := systemBackupListRequest(t, openSystemHandlerTestDB(t), "?source="+string(source))
			if status != http.StatusServiceUnavailable || code != "BACKUP_SCAN_LIMIT_EXCEEDED" {
				t.Fatalf("checksum exhaustion response = %d %s", status, code)
			}
		})
	}
}

func TestSystemBackupListTooLargeAndPackagedSize(t *testing.T) {
	directory := t.TempDir()
	writeSystemBackupTestPair(t, directory, systemBackupTestWebName, "")
	if err := os.Truncate(filepath.Join(directory, systemBackupTestWebName), systemBackupMaxDataBytes+1); err != nil {
		t.Fatal(err)
	}
	entries, err := listSystemBackupFiles(directory, systemBackupSourceWeb, "sqlite")
	if err != nil || len(entries) != 1 || entries[0].Downloadable || entries[0].UnavailableReason != "too_large" || entries[0].PackagedSize != 2560+systemBackupMaxDataBytes+512 {
		t.Fatalf("oversize metadata = %+v %v", entries, err)
	}
	for size, want := range map[int64]int64{0: 0, -1: 0, 1: 3072, 511: 3072, 512: 3072, 513: 3584, int64(^uint64(0) >> 1): 0} {
		if got := systemBackupPackagedSize(size); got != want {
			t.Errorf("packaged size(%d) = %d, want %d", size, got, want)
		}
	}
}

func TestSystemBackupPinChecksumBoundaryAndCloseOwnership(t *testing.T) {
	directory := t.TempDir()
	name := "xirang-sqlite-20261017-120000.db"
	digest, _ := writeSystemBackupTestPair(t, directory, name, "")
	label := strings.Repeat("x", systemBackupMaxChecksumBytes-64-2-1-len(name)-1) + "/" + name
	record := []byte(digest + "  " + label + "\n")
	if len(record) != systemBackupMaxChecksumBytes {
		t.Fatalf("boundary fixture size = %d", len(record))
	}
	writeSystemBackupTestFile(t, filepath.Join(directory, name+".sha256"), record)
	root, err := openSystemBackupRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.root.Close() }()
	budget := &systemBackupScanBudget{}
	pair, err := root.pinPair(systemBackupSourceCron, "sqlite", name, budget)
	if err != nil {
		t.Fatal(err)
	}
	if pair.SHA256 != digest || budget.checksumBytes != systemBackupMaxChecksumBytes {
		t.Fatalf("boundary digest/budget = %q %d", pair.SHA256, budget.checksumBytes)
	}
	file := pair.File
	if err := pair.Close(); err != nil {
		t.Fatal(err)
	}
	var buffer [1]byte
	if _, err := file.Read(buffer[:]); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("pair.Close did not close owned data descriptor: %v", err)
	}
}

func TestSystemBackupSockets(t *testing.T) {
	directory := t.TempDir()
	listener, err := net.Listen("unix", filepath.Join(directory, systemBackupTestWebName))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	if pair, err := pinSystemBackupFilePair(directory, systemBackupSourceWeb, "sqlite", systemBackupTestWebName); pair != nil || !errors.Is(err, errSystemBackupUnsafe) {
		if pair != nil {
			_ = pair.Close()
		}
		t.Fatalf("socket pin = %+v %v", pair, err)
	}
	entries, err := listSystemBackupFiles(directory, systemBackupSourceWeb, "sqlite")
	if err != nil || len(entries) != 0 {
		t.Fatalf("socket exposed as list metadata: %+v %v", entries, err)
	}
}

func TestSystemBackupCronIncompletePairsStayVisible(t *testing.T) {
	directory := t.TempDir()
	missing := "xirang-sqlite-20261017-120000.db"
	empty := "xirang-sqlite-20261017-110000.db"
	malformed := "xirang-sqlite-20261017-100000.db"
	writeSystemBackupTestFile(t, filepath.Join(directory, missing), []byte("FAKE_BACKUP_FOR_TEST_ONLY"))
	writeSystemBackupTestFile(t, filepath.Join(directory, empty), nil)
	writeSystemBackupTestPair(t, directory, malformed, "")
	writeSystemBackupTestFile(t, filepath.Join(directory, malformed+".sha256"), []byte("malformed receipt"))
	for _, name := range []string{
		"xirang-postgres-20261017-120000.dump",
		"xirang-sqlite-20260230-120000.db",
		"xirang-sqlite-20261017-120000.db.tmp",
		"unmanaged.db",
	} {
		writeSystemBackupTestFile(t, filepath.Join(directory, name), []byte("FAKE_UNMANAGED_BACKUP_FOR_TEST_ONLY"))
	}
	entries, err := listSystemBackupFiles(directory, systemBackupSourceCron, "sqlite")
	if err != nil || len(entries) != 3 {
		t.Fatalf("incomplete cron list = %+v %v", entries, err)
	}
	for _, entry := range entries {
		if entry.UnavailableReason != "incomplete" || entry.Downloadable || entry.SHA256 != "" || entry.Source != systemBackupSourceCron || entry.Engine != "sqlite" {
			t.Fatalf("incomplete cron row = %+v", entry)
		}
	}
}
