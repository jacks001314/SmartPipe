package mcp

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// Agent 模板里写错工具名，Agent 到运行时才发现，代价是一次失败的派工。
// 这里把模板中反引号里的工具名和真实工具清单对齐。
var templateToolRef = regexp.MustCompile("`((?:project|issue|verify|derivation|release|repo)\\.[a-z0-9_]+)`")

func TestAgentTemplatesReferenceRealTools(t *testing.T) {
	root := filepath.Join("..", "..", "..", "configs", "agents")
	dirs, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read templates: %v", err)
	}
	known := map[string]bool{}
	for _, tool := range Tools {
		known[tool.Name] = true
	}

	checked := 0
	for _, dir := range dirs {
		if !dir.IsDir() {
			continue
		}
		path := filepath.Join(root, dir.Name(), "AGENTS.md")
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for _, m := range templateToolRef.FindAllStringSubmatch(string(raw), -1) {
			name := m[1]
			checked++
			if !known[name] {
				t.Errorf("%s references unknown tool %q", path, name)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no tool references found in templates; path layout may have changed")
	}
}