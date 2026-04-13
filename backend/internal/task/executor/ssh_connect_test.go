package executor

import (
	"testing"
	"xirang/backend/internal/model"
)

func TestResolveSSHUserReturnsConfiguredUsername(t *testing.T) {
	node := model.Node{Name: "test-node", Username: "deploy"}
	user := ResolveSSHUser(node)
	if user != "deploy" {
		t.Fatalf("期望用户名=deploy，实际=%s", user)
	}
}

func TestResolveSSHUserDefaultsToRootWhenEmpty(t *testing.T) {
	node := model.Node{Name: "test-node", Username: ""}
	user := ResolveSSHUser(node)
	if user != "root" {
		t.Fatalf("期望空用户名回退到 root，实际=%s", user)
	}
}

func TestResolveSSHUserTrimsWhitespace(t *testing.T) {
	node := model.Node{Name: "test-node", Username: "  admin  "}
	user := ResolveSSHUser(node)
	if user != "admin" {
		t.Fatalf("期望去除空白后=admin，实际=%s", user)
	}
}

func TestResolveSSHAuthMethodsRejectsEmptyAuthType(t *testing.T) {
	node := model.Node{Name: "test-node", AuthType: ""}
	_, err := resolveSSHAuthMethods(node)
	if err == nil {
		t.Fatal("期望空认证类型报错")
	}
}

func TestResolveSSHAuthMethodsRejectsPasswordWithoutPassword(t *testing.T) {
	node := model.Node{Name: "test-node", AuthType: "password", Password: ""}
	_, err := resolveSSHAuthMethods(node)
	if err == nil {
		t.Fatal("期望无密码时报错")
	}
}

func TestResolveSSHAuthMethodsRejectsKeyWithoutKey(t *testing.T) {
	node := model.Node{Name: "test-node", AuthType: "key"}
	_, err := resolveSSHAuthMethods(node)
	if err == nil {
		t.Fatal("期望无密钥时报错")
	}
}
