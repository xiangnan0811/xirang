package internal_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSelectedRuntimeFilesDoNotUseStandardLog(t *testing.T) {
	files := []string{
		filepath.Join("api", "handlers", "node_handler.go"),
		filepath.Join("policy", "sync.go"),
	}
	for _, file := range files {
		source, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		for _, disallowed := range []string{"log.Printf(", "log.Println("} {
			if strings.Contains(string(source), disallowed) {
				t.Errorf("%s contains %s", file, disallowed)
			}
		}
	}
}

func TestScopedAlertDeliveryPathsDoNotUsePackageLevelSendShims(t *testing.T) {
	files := []string{
		filepath.Join("api", "handlers", "alert_handler.go"),
		filepath.Join("integration", "service.go"),
	}
	for _, file := range files {
		source, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		for _, disallowed := range []string{"alerting.SendAlert(", "alerting.SendProbe("} {
			if strings.Contains(string(source), disallowed) {
				t.Errorf("%s contains %s", file, disallowed)
			}
		}
	}
}
