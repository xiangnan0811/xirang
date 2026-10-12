package model

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestBatchCommandTaskFingerprintCanonicalV1(t *testing.T) {
	task := Task{
		ID:           17,
		NodeID:       29,
		BatchID:      "batch-abc",
		Source:       "batch",
		ExecutorType: "command",
		Command:      "echo hello",
	}
	if got, want := BatchCommandTaskFingerprint(task), "7c5a0c9f5765aa09a693bddfa653001ad81495d7d21a930398d041b3ee17e4ff"; got != want {
		t.Fatalf("fingerprint=%q, want canonical v1 digest %q", got, want)
	}

	ignored := task
	ignored.Name = "renamed"
	ignored.Status = TaskRunStatusFailed
	ignored.Enabled = false
	ignored.CreatedAt = task.UpdatedAt.Add(24 * time.Hour)
	ignored.Node.Name = "rotated-node-name"
	if got, want := BatchCommandTaskFingerprint(ignored), BatchCommandTaskFingerprint(task); got != want {
		t.Fatalf("mutable labels/status/times/node details changed fingerprint: got %q want %q", got, want)
	}

	withWhitespace := task
	withWhitespace.Command = " echo hello "
	if got := BatchCommandTaskFingerprint(withWhitespace); got == BatchCommandTaskFingerprint(task) {
		t.Fatal("command whitespace must remain part of the canonical command bytes")
	}
}

func TestBatchCommandTaskFingerprintRejectsUnsupportedShape(t *testing.T) {
	zero := uint(0)
	cases := []struct {
		name   string
		mutate func(*Task)
	}{
		{name: "missing task id", mutate: func(task *Task) { task.ID = 0 }},
		{name: "missing node id", mutate: func(task *Task) { task.NodeID = 0 }},
		{name: "missing batch id", mutate: func(task *Task) { task.BatchID = "" }},
		{name: "non-batch source", mutate: func(task *Task) { task.Source = "manual" }},
		{name: "non-command executor", mutate: func(task *Task) { task.ExecutorType = "local" }},
		{name: "missing command", mutate: func(task *Task) { task.Command = "" }},
		{name: "policy bound", mutate: func(task *Task) { task.PolicyID = &zero }},
		{name: "dependency bound", mutate: func(task *Task) { task.DependsOnTaskID = &zero }},
		{name: "cron bound", mutate: func(task *Task) { task.CronSpec = "* * * * *" }},
		{name: "rsync source bound", mutate: func(task *Task) { task.RsyncSource = "/source" }},
		{name: "rsync target bound", mutate: func(task *Task) { task.RsyncTarget = "/target" }},
		{name: "executor config bound", mutate: func(task *Task) { task.ExecutorConfig = "{}" }},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			task := Task{
				ID:           17,
				NodeID:       29,
				BatchID:      "batch-abc",
				Source:       "batch",
				ExecutorType: "command",
				Command:      "echo hello",
			}
			testCase.mutate(&task)
			if got := BatchCommandTaskFingerprint(task); got != "" {
				t.Fatalf("unsupported task shape produced fingerprint %q", got)
			}
		})
	}
}

func TestBatchCommandRetryInternalFieldsAreJSONPrivate(t *testing.T) {
	sourceBatchID := "source-batch"
	retryTaskID := uint(17)
	retryRunID := uint(29)
	encoded, err := json.Marshal(struct {
		Batch    BatchCommand            `json:"batch"`
		Dispatch BatchCommandDispatch    `json:"dispatch"`
		Evidence BatchCommandRunEvidence `json:"evidence"`
	}{
		Batch: BatchCommand{ProvenanceVersion: 1, SourceBatchID: &sourceBatchID},
		Dispatch: BatchCommandDispatch{
			CommandFingerprint: "0123456789abcdef",
			RetryOfTaskID:      &retryTaskID,
			RetryOfRunID:       &retryRunID,
		},
		Evidence: BatchCommandRunEvidence{RunID: 29, TaskID: 17, NodeID: 29, CommandFingerprint: "0123456789abcdef", Outcome: "unknown"},
	})
	if err != nil {
		t.Fatalf("marshal models: %v", err)
	}
	payload := string(encoded)
	for _, privateField := range []string{"provenance_version", "source_batch_id", "command_fingerprint", "retry_of_task_id", "retry_of_run_id", "outcome"} {
		if strings.Contains(payload, privateField) {
			t.Fatalf("private field %q appeared in JSON: %s", privateField, payload)
		}
	}
}
