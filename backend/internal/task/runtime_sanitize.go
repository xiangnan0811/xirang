package task

import (
	"strings"

	"xirang/backend/internal/runtimeevidence"
)

func sanitizeTaskLogMessage(message string) string {
	return sanitizeTaskRuntimeEvidence(message)
}

// SanitizeRuntimeEvidenceForRead sanitizes stored task evidence for API reads.
func SanitizeRuntimeEvidenceForRead(message string) string {
	return sanitizeTaskRuntimeEvidence(message)
}

func sanitizeTaskLastError(message string) string {
	return sanitizeTaskRuntimeEvidence(message)
}

func sanitizeTaskRuntimeError(err error) string {
	if err == nil {
		return ""
	}
	return sanitizeTaskLastError(err.Error())
}

func sanitizeTaskRuntimeOutput(output string) string {
	if strings.TrimSpace(output) == "" {
		return ""
	}
	return "[输出已隐藏]"
}

func sanitizeTaskRuntimeEvidence(message string) string {
	return runtimeevidence.SanitizeTaskRuntimeEvidence(message)
}
