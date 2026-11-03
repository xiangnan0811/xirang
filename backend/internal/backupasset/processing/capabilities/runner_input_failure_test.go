package capabilities

import (
	"context"
	"errors"
	"io"
	"os"
	"testing"
	"time"
)

type terminalMaterializeFailure struct {
	data []byte
}

func (reader *terminalMaterializeFailure) Read(p []byte) (int, error) {
	if len(reader.data) == 0 {
		return 0, errors.New("input failed at end")
	}
	n := copy(p, reader.data)
	reader.data = reader.data[n:]
	if len(reader.data) == 0 {
		return n, errors.New("input failed at end")
	}
	return n, nil
}

func TestRunnerRejectsTerminalInputErrorBeforeResolvingOrStartingTool(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	resolved := false
	runner := newRunnerForTest(RunnerConfig{
		WorkspaceRoot: root, StdoutLimit: 1024, StderrLimit: 1024, GracePeriod: 100 * time.Millisecond,
	}, func(ExecutableID) (string, error) { resolved = true; return "/bin/false", nil })
	invocation := ToolInvocation{
		ExecutableID: ExecutableClamScan, ArgProfile: ArgsClamScan, InputMode: ToolInputPath,
		OutputSpec: ClosedOutputSpec{MaximumFiles: 1, AllowedNames: []string{"metadata.json"}},
		Limits:     testToolLimits(time.Second, 1024, 1024), SuccessExitCodes: []int{0, 1},
	}
	result, err := runner.RunInput(context.Background(), invocation, &terminalMaterializeFailure{data: []byte("all source bytes")})
	if err == nil || resolved || len(result.Outputs) != 0 {
		t.Fatalf("terminal input error=%v resolved=%t result=%+v", err, resolved, result)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("workspace cleanup entries=%v err=%v", entries, err)
	}
}

var _ io.Reader = (*terminalMaterializeFailure)(nil)
