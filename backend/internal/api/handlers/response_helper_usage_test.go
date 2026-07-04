package handlers

import (
	"os"
	"strings"
	"testing"
)

func TestSelectedHandlersAvoidDirectJSONResponses(t *testing.T) {
	t.Parallel()

	files := []string{
		"auth_handler.go",
		"node_handler.go",
		"policy_handler.go",
	}

	for _, file := range files {
		file := file
		t.Run(file, func(t *testing.T) {
			t.Parallel()

			content, err := os.ReadFile(file)
			if err != nil {
				t.Fatalf("读取 %s 失败: %v", file, err)
			}
			if strings.Contains(string(content), "c.JSON(") {
				t.Fatalf("%s should use response helpers instead of direct c.JSON", file)
			}
		})
	}
}
