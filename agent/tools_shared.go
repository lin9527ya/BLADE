package agent

import (
	"os"
	"path/filepath"
	"strings"
)

// sharedDir is the task-scoped directory shared by all intents:
// <workDir>/tasks/<taskID>/shared. runDir is normally i<intentID> or a slot dir.
func (t *ToolSet) sharedDir() string {
	if strings.TrimSpace(t.runDir) == "" {
		return ""
	}
	root := filepath.Dir(t.runDir)
	if filepath.Base(root) == "shared" {
		root = filepath.Dir(root)
	}
	dir := filepath.Join(root, "shared")
	if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0o755); err != nil {
		return ""
	}
	return dir
}

func sanitizeScriptName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.TrimPrefix(name, "/")
	clean := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
			return r
		}
		return -1
	}, name)
	if clean == "" || clean == "." || clean == ".." || len(clean) > 120 {
		return ""
	}
	return clean
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
