package api

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func systemBackupNginxLocation(t *testing.T, template, opening string) []string {
	t.Helper()
	if strings.Count(template, opening) != 1 {
		t.Fatalf("expected one location %q", opening)
	}
	_, rest, _ := strings.Cut(template, opening)
	body, _, ok := strings.Cut(rest, "\n  }")
	if !ok {
		t.Fatalf("unterminated location %q", opening)
	}
	var directives []string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			directives = append(directives, line)
		}
	}
	return directives
}

func TestSystemBackupDownloadNginxStreamingContract(t *testing.T) {
	data, err := os.ReadFile("../../../deploy/nginx/templates/default.conf.template")
	if err != nil {
		t.Fatal(err)
	}
	template := string(data)
	const pattern = `^/api/v1/system/backups/(web|cron)/[^/]+/download$`
	const opening = `location ~ "` + pattern + `" {`
	directives := systemBackupNginxLocation(t, template, opening)
	want := []string{
		"proxy_pass http://127.0.0.1:3000;",
		"proxy_http_version 1.1;",
		"proxy_set_header Host $host;",
		"proxy_set_header X-Real-IP $remote_addr;",
		"proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;",
		"proxy_set_header X-Forwarded-Proto $scheme;",
		"proxy_buffering off;",
		"proxy_request_buffering off;",
		"proxy_cache off;",
		"proxy_max_temp_file_size 0;",
		"gzip off;",
		"proxy_read_timeout 660s;",
		"proxy_send_timeout 30s;",
		"send_timeout 30s;",
	}
	if strings.Join(directives, "\n") != strings.Join(want, "\n") {
		t.Fatalf("R14 proxy contract changed:\ngot %s\nwant %s", strings.Join(directives, "\n"), strings.Join(want, "\n"))
	}
	if strings.Index(template, opening) > strings.Index(template, "location /api/v1/ {") {
		t.Fatal("download-specific policy must precede the ordinary API location")
	}
	matcher := regexp.MustCompile(pattern)
	for _, path := range []string{
		"/api/v1/system/backups/web/xirang-20261017-120000.db/download",
		"/api/v1/system/backups/cron/xirang-postgres-20261017-120000.dump/download",
	} {
		if !matcher.MatchString(path) {
			t.Fatalf("download route missed streaming proxy: %s", path)
		}
	}
	for _, path := range []string{
		"/api/v1/system/backups",
		"/api/v1/system/backups?source=cron",
		"/api/v1/system/backups/other/file.db/download",
		"/api/v1/system/backups/web//download",
		"/api/v1/system/backups/web/dir/file.db/download",
		"/api/v1/system/backups/web/file.db/download/",
		"/api/v1/credential-access-grants/system-backup-download",
		"/api/v1/asset-content/0123456789abcdef0123456789abcdef",
	} {
		if matcher.MatchString(path) {
			t.Fatalf("unrelated route acquired R14 streaming policy: %s", path)
		}
	}
}

func TestSystemBackupDownloadNginxKeepsOrdinaryAndContentPolicies(t *testing.T) {
	data, err := os.ReadFile("../../../deploy/nginx/templates/default.conf.template")
	if err != nil {
		t.Fatal(err)
	}
	template := string(data)
	generic := systemBackupNginxLocation(t, template, "location /api/v1/ {")
	wantGeneric := []string{
		"client_max_body_size 16m;",
		"proxy_pass http://127.0.0.1:3000;",
		"proxy_http_version 1.1;",
		"proxy_set_header Host $host;",
		"proxy_set_header X-Real-IP $remote_addr;",
		"proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;",
		"proxy_set_header X-Forwarded-Proto $scheme;",
		"proxy_set_header Upgrade $http_upgrade;",
		"proxy_set_header Connection $connection_upgrade;",
		"proxy_read_timeout 3600s;",
	}
	if strings.Join(generic, "\n") != strings.Join(wantGeneric, "\n") {
		t.Fatalf("ordinary API proxy changed: %v", generic)
	}
	content := strings.Join(systemBackupNginxLocation(t, template, `location ~ "^/api/v1/asset-content/[0-9a-f]{32}$" {`), "\n")
	for _, directive := range []string{
		"access_log /logs/nginx-asset-content.log xirang_asset_content;",
		"error_log /dev/null crit;",
		"proxy_read_timeout 75s;", "proxy_send_timeout 75s;", "send_timeout 75s;",
		"proxy_set_header Host $http_host;", `proxy_set_header X-Real-IP "";`,
		"proxy_set_header Range $http_range;", "proxy_set_header If-Range $http_if_range;",
	} {
		if !strings.Contains(content, directive) {
			t.Fatalf("content gateway lost its independent contract: %s", directive)
		}
	}
	if strings.Index(template, `location ~ "^/api/v1/asset-content/`) > strings.Index(template, `location ~ "^/api/v1/system/backups/`) {
		t.Fatal("R14 must not take precedence over the independent content gateway")
	}
}
