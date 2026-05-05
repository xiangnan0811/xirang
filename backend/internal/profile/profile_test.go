package profile

import (
	"strings"
	"testing"
)

func TestBuiltinProfilesCount(t *testing.T) {
	if len(BuiltinProfiles) != 8 {
		t.Errorf("expected 8 builtin profiles, got %d", len(BuiltinProfiles))
	}
}

func TestGetProfile(t *testing.T) {
	p, ok := GetProfile("mysql")
	if !ok {
		t.Fatal("expected mysql profile to exist")
	}
	if p.ID != "mysql" {
		t.Errorf("expected ID 'mysql', got '%s'", p.ID)
	}
	if p.CredentialType != "mysql" {
		t.Errorf("expected credential_type 'mysql', got '%s'", p.CredentialType)
	}
	if p.IsDocker {
		t.Error("mysql profile should not be docker")
	}
}

func TestGetProfileNotFound(t *testing.T) {
	_, ok := GetProfile("nonexistent")
	if ok {
		t.Error("expected false for nonexistent profile")
	}
}

func TestListProfiles(t *testing.T) {
	profiles := ListProfiles()
	if len(profiles) != 8 {
		t.Errorf("expected 8 profiles, got %d", len(profiles))
	}
	// 验证返回的 profile 包含公开字段
	for _, p := range profiles {
		if p.ID == "" {
			t.Error("profile ID should not be empty")
		}
		if p.Name == "" {
			t.Errorf("profile %s name should not be empty", p.ID)
		}
		if p.CredentialType == "" {
			t.Errorf("profile %s credential_type should not be empty", p.ID)
		}
	}
}

func TestRenderHooksMySQL(t *testing.T) {
	cfg := map[string]interface{}{
		"host":     "10.0.0.1",
		"port":     "3306",
		"user":     "root",
		"password": "FAKE_MYSQL_PW_FOR_TEST_ONLY",
	}
	pre, post, err := RenderHooks("mysql", cfg)
	if err != nil {
		t.Fatalf("RenderHooks mysql: %v", err)
	}
	if !strings.Contains(pre, "mysqldump") {
		t.Error("pre-hook should contain mysqldump")
	}
	if !strings.Contains(pre, "-u root") {
		t.Error("pre-hook should contain -u root")
	}
	if !strings.Contains(pre, "-h 10.0.0.1") {
		t.Error("pre-hook should contain -h 10.0.0.1")
	}
	if !strings.Contains(pre, "-P 3306") {
		t.Error("pre-hook should contain -P 3306")
	}
	if !strings.Contains(pre, "-p'FAKE_MYSQL_PW_FOR_TEST_ONLY'") {
		t.Error("pre-hook should contain password")
	}
	if !strings.Contains(pre, "--all-databases") {
		t.Error("pre-hook should contain --all-databases")
	}
	if !strings.Contains(pre, "--single-transaction") {
		t.Error("pre-hook should contain --single-transaction")
	}
	if !strings.Contains(post, "rm -f /tmp/xirang-mysql-backup.sql") {
		t.Error("post-hook should contain cleanup command")
	}
}

func TestRenderHooksMySQLMinimal(t *testing.T) {
	// 最小 config（无 host/port/user/password）不应 panic
	cfg := map[string]interface{}{}
	pre, post, err := RenderHooks("mysql", cfg)
	if err != nil {
		t.Fatalf("RenderHooks mysql minimal: %v", err)
	}
	if !strings.Contains(pre, "mysqldump") {
		t.Error("pre-hook should contain mysqldump even with minimal config")
	}
	if strings.Contains(pre, "FAKE_MYSQL_PW_FOR_TEST_ONLY") {
		t.Error("pre-hook should not contain password from previous test")
	}
	if !strings.Contains(post, "rm -f") {
		t.Error("post-hook should contain rm -f")
	}
}

func TestRenderHooksPostgres(t *testing.T) {
	cfg := map[string]interface{}{
		"host":     "10.0.0.2",
		"port":     "5432",
		"user":     "myuser",
		"password": "FAKE_PG_PW_FOR_TEST_ONLY",
	}
	pre, post, err := RenderHooks("postgres", cfg)
	if err != nil {
		t.Fatalf("RenderHooks postgres: %v", err)
	}
	if !strings.Contains(pre, "pg_dumpall") {
		t.Error("pre-hook should contain pg_dumpall")
	}
	if !strings.Contains(pre, "PGPASSWORD='FAKE_PG_PW_FOR_TEST_ONLY'") {
		t.Error("pre-hook should contain PGPASSWORD env var")
	}
	if !strings.Contains(pre, "-h 10.0.0.2") {
		t.Error("pre-hook should contain -h")
	}
	if !strings.Contains(pre, "-U myuser") {
		t.Error("pre-hook should contain -U")
	}
	if !strings.Contains(post, "rm -f /tmp/xirang-pg-backup.sql") {
		t.Error("post-hook should contain cleanup command")
	}
}

func TestRenderHooksMongoDB(t *testing.T) {
	cfg := map[string]interface{}{
		"host":     "10.0.0.3",
		"port":     "27017",
		"user":     "admin",
		"password": "FAKE_MONGO_PW_FOR_TEST_ONLY",
	}
	pre, post, err := RenderHooks("mongodb", cfg)
	if err != nil {
		t.Fatalf("RenderHooks mongodb: %v", err)
	}
	if !strings.Contains(pre, "mongodump") {
		t.Error("pre-hook should contain mongodump")
	}
	if !strings.Contains(pre, "--host 10.0.0.3") {
		t.Error("pre-hook should contain --host")
	}
	if !strings.Contains(pre, "--username admin") {
		t.Error("pre-hook should contain --username")
	}
	if !strings.Contains(pre, "--password 'FAKE_MONGO_PW_FOR_TEST_ONLY'") {
		t.Error("pre-hook should contain password")
	}
	if !strings.Contains(post, "rm -rf /tmp/xirang-mongo-backup") {
		t.Error("post-hook should contain rm -rf")
	}
}

func TestRenderHooksRedis(t *testing.T) {
	cfg := map[string]interface{}{
		"host":     "10.0.0.4",
		"port":     "6379",
		"password": "FAKE_REDIS_PW_FOR_TEST_ONLY",
	}
	pre, post, err := RenderHooks("redis", cfg)
	if err != nil {
		t.Fatalf("RenderHooks redis: %v", err)
	}
	if !strings.Contains(pre, "redis-cli") {
		t.Error("pre-hook should contain redis-cli")
	}
	if !strings.Contains(pre, "BGSAVE") {
		t.Error("pre-hook should contain BGSAVE")
	}
	if !strings.Contains(pre, "-a 'FAKE_REDIS_PW_FOR_TEST_ONLY'") {
		t.Error("pre-hook should contain password")
	}
	if !strings.Contains(pre, "cp /var/lib/redis/dump.rdb") {
		t.Error("pre-hook should contain cp command")
	}
	if !strings.Contains(post, "rm -f /tmp/xirang-redis-backup.rdb") {
		t.Error("post-hook should contain cleanup command")
	}
}

func TestRenderHooksDockerMySQL(t *testing.T) {
	cfg := map[string]interface{}{
		"container_name": "my-mysql",
		"user":           "root",
		"password":       "FAKE_DOCKER_MYSQL_PW_FOR_TEST_ONLY",
	}
	pre, post, err := RenderHooks("docker-mysql", cfg)
	if err != nil {
		t.Fatalf("RenderHooks docker-mysql: %v", err)
	}
	// 验证容器存在性预校验
	if !strings.Contains(pre, "docker inspect my-mysql") {
		t.Error("pre-hook should contain docker inspect for container existence check")
	}
	if !strings.Contains(pre, "容器 my-mysql 不存在或未运行") {
		t.Error("pre-hook should contain Chinese error message for missing container")
	}
	if !strings.Contains(pre, "docker exec my-mysql mysqldump") {
		t.Error("pre-hook should contain docker exec mysqldump")
	}
	if !strings.Contains(pre, "-u root") {
		t.Error("pre-hook should contain -u root")
	}
	if !strings.Contains(pre, "-p'FAKE_DOCKER_MYSQL_PW_FOR_TEST_ONLY'") {
		t.Error("pre-hook should contain password")
	}
	if !strings.Contains(post, "rm -f /tmp/xirang-docker-mysql-backup.sql") {
		t.Error("post-hook should contain cleanup")
	}
}

func TestRenderHooksDockerPostgres(t *testing.T) {
	cfg := map[string]interface{}{
		"container_name": "my-pg",
		"user":           "myuser",
	}
	pre, post, err := RenderHooks("docker-postgres", cfg)
	if err != nil {
		t.Fatalf("RenderHooks docker-postgres: %v", err)
	}
	if !strings.Contains(pre, "docker inspect my-pg") {
		t.Error("pre-hook should contain docker inspect")
	}
	if !strings.Contains(pre, "docker exec my-pg pg_dumpall") {
		t.Error("pre-hook should contain docker exec pg_dumpall")
	}
	if !strings.Contains(pre, "-U myuser") {
		t.Error("pre-hook should contain -U myuser")
	}
	if !strings.Contains(post, "rm -f /tmp/xirang-docker-pg-backup.sql") {
		t.Error("post-hook should contain cleanup")
	}
}

func TestRenderHooksDockerMongoDB(t *testing.T) {
	cfg := map[string]interface{}{
		"container_name": "my-mongo",
		"user":           "admin",
		"password":       "FAKE_DOCKER_MONGO_PW_FOR_TEST_ONLY",
	}
	pre, post, err := RenderHooks("docker-mongodb", cfg)
	if err != nil {
		t.Fatalf("RenderHooks docker-mongodb: %v", err)
	}
	if !strings.Contains(pre, "docker inspect my-mongo") {
		t.Error("pre-hook should contain docker inspect")
	}
	if !strings.Contains(pre, "docker exec my-mongo mongodump") {
		t.Error("pre-hook should contain docker exec mongodump")
	}
	if !strings.Contains(pre, "--password 'FAKE_DOCKER_MONGO_PW_FOR_TEST_ONLY'") {
		t.Error("pre-hook should contain password")
	}
	if !strings.Contains(post, "rm -rf /tmp/xirang-docker-mongo-backup") {
		t.Error("post-hook should contain cleanup")
	}
}

func TestRenderHooksDockerRedis(t *testing.T) {
	cfg := map[string]interface{}{
		"container_name": "my-redis",
		"password":       "FAKE_DOCKER_REDIS_PW_FOR_TEST_ONLY",
	}
	pre, post, err := RenderHooks("docker-redis", cfg)
	if err != nil {
		t.Fatalf("RenderHooks docker-redis: %v", err)
	}
	if !strings.Contains(pre, "docker inspect my-redis") {
		t.Error("pre-hook should contain docker inspect")
	}
	if !strings.Contains(pre, "docker exec my-redis redis-cli") {
		t.Error("pre-hook should contain docker exec redis-cli")
	}
	if !strings.Contains(pre, "BGSAVE") {
		t.Error("pre-hook should contain BGSAVE")
	}
	if !strings.Contains(pre, "docker cp my-redis:/data/dump.rdb") {
		t.Error("pre-hook should contain docker cp")
	}
	if !strings.Contains(post, "rm -f /tmp/xirang-docker-redis-backup.rdb") {
		t.Error("post-hook should contain cleanup")
	}
}

func TestRenderHooksUnknownProfile(t *testing.T) {
	_, _, err := RenderHooks("oracle", map[string]interface{}{})
	if err == nil {
		t.Error("expected error for unknown profile")
	}
}

func TestRenderHooksNoPassword(t *testing.T) {
	// 无密码时模板不应对 password 字段 panic
	cfg := map[string]interface{}{
		"host": "10.0.0.1",
		"user": "root",
	}
	_, _, err := RenderHooks("mysql", cfg)
	if err != nil {
		t.Fatalf("RenderHooks mysql without password: %v", err)
	}
}

func TestDockerProfilesAllHaveInspectCheck(t *testing.T) {
	dockerProfiles := []string{"docker-mysql", "docker-postgres", "docker-mongodb", "docker-redis"}
	for _, id := range dockerProfiles {
		p, ok := GetProfile(id)
		if !ok {
			t.Fatalf("profile %s not found", id)
		}
		if !p.IsDocker {
			t.Errorf("profile %s should have IsDocker=true", id)
		}
		// 验证 pre-hook template 第一行是 docker inspect
		if !strings.HasPrefix(strings.TrimSpace(p.PreHookTemplate), "docker inspect") {
			t.Errorf("profile %s pre-hook should start with docker inspect, got: %s", id, p.PreHookTemplate[:min(30, len(p.PreHookTemplate))])
		}
	}
}

func TestHostProfilesNotDocker(t *testing.T) {
	hostProfiles := []string{"mysql", "postgres", "mongodb", "redis"}
	for _, id := range hostProfiles {
		p, ok := GetProfile(id)
		if !ok {
			t.Fatalf("profile %s not found", id)
		}
		if p.IsDocker {
			t.Errorf("profile %s should have IsDocker=false", id)
		}
	}
}

func TestPasswordInRenderedHook(t *testing.T) {
	// 验证密码确实出现在渲染后的 hook 中（设计如此：hook 在目标节点上以明文执行）
	cfg := map[string]interface{}{
		"password": "FAKE_PASSWORD_FOR_TEST_ONLY",
	}
	pre, _, err := RenderHooks("mysql", cfg)
	if err != nil {
		t.Fatalf("RenderHooks: %v", err)
	}
	if !strings.Contains(pre, "FAKE_PASSWORD_FOR_TEST_ONLY") {
		t.Error("password should appear in rendered hook")
	}
}

func TestTemplateNoInjection(t *testing.T) {
	// 验证模板变量不会导致 injection panic（text/template 安全）
	cfg := map[string]interface{}{
		"host":     "{{.Bad}}",
		"password": "$(evil)",  // shell injection — but this is expected, as we output user password directly
		"user":     "normal_user",
	}
	pre, _, err := RenderHooks("mysql", cfg)
	if err != nil {
		t.Fatalf("RenderHooks with special chars: %v", err)
	}
	// template 安全性：{{.Bad}} 作为 host 字段的值被原样输出，不会被二次解析为模板。
	// text/template 只执行一次，输出中的 {{ }} 只是普通文本，不会再次被求值。
	if !strings.Contains(pre, "-h {{.Bad}}") {
		t.Error("template should render host value literally, even if it contains template-looking text")
	}
}

func TestCredentialTypeMatch(t *testing.T) {
	// 每个 profile 的 CredentialType 必须与 ID 对应
	for _, p := range BuiltinProfiles {
		expectedType := p.ID // docker-* profiles 的 CredentialType 也是 docker-*
		if p.CredentialType != expectedType {
			t.Errorf("profile %s: CredentialType %s should match ID %s", p.ID, p.CredentialType, expectedType)
		}
	}
}
