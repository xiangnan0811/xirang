package executor

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	"xirang/backend/internal/model"
)

func TestLegacyRsyncPassesBracketedIPv6OperandToProcess(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	privateKey := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	binary := filepath.Join(t.TempDir(), "check-rsync-operand")
	script := "#!/bin/sh\nprevious=''\ncurrent=''\nfor arg in \"$@\"; do previous=\"$current\"; current=\"$arg\"; done\n[ \"$previous\" = 'root@[2001:db8::10]:/var/data' ]\n"
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	task := model.Task{ExecutorType: "rsync", RsyncSource: "/var/data", RsyncTarget: t.TempDir(), Node: model.Node{Host: "2001:db8::10", Port: 22, Username: "root", AuthType: "key", SSHKey: &model.SSHKey{PrivateKey: string(privateKey)}}}
	code, err := (&RsyncExecutor{binary: binary}).Run(context.Background(), task, func(string, string) {}, nil)
	if err != nil || code != 0 {
		t.Fatalf("rsync consumer rejected remote operand: exit=%d err=%v", code, err)
	}
}
