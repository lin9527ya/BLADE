package server

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/lin9527ya/Norma/skill"
)

func coldCoffeeRepoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Dir(filepath.Dir(file))
}

func nonEmptyReferenceLines(text string) []string {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	return out
}

func TestColdCoffeeSkillPackage(t *testing.T) {
	root := coldCoffeeRepoRoot(t)
	skillFile := filepath.Join(root, "skills", coldCoffeeSkillName, "SKILL.md")
	data, err := os.ReadFile(skillFile)
	if err != nil {
		t.Fatalf("read SKILL.md: %v", err)
	}
	if err := validateSkillFrontmatter(data); err != nil {
		t.Fatalf("frontmatter guard: %v", err)
	}

	reg, err := skill.LoadDir(filepath.Join(root, "skills"))
	if err != nil {
		t.Fatalf("skill.LoadDir: %v", err)
	}
	s, ok := reg.Get(coldCoffeeSkillName)
	if !ok {
		t.Fatalf("built-in skill %q not found under skills/", coldCoffeeSkillName)
	}
	if s.Name != coldCoffeeSkillName || filepath.Base(s.Dir) != coldCoffeeSkillName {
		t.Fatalf("skill name/dir mismatch: name=%q dir=%q", s.Name, filepath.Base(s.Dir))
	}
	if s.Description == "" || strings.Contains(s.Description, "\n") {
		t.Fatalf("description must be a non-empty single line: %q", s.Description)
	}
	if len([]byte(s.Description)) > 500 {
		t.Fatalf("description is %d bytes, Norma clips at 500", len([]byte(s.Description)))
	}
	if !strings.Contains(s.Description, "冷咖啡") || !strings.Contains(s.Description, "cold coffee") {
		t.Fatalf("description must carry both activation words: %q", s.Description)
	}
	if !slices.Contains(s.MCPs, "ida-pro-mcp") {
		t.Fatalf("skill must declare mcps: ida-pro-mcp, got %v", s.MCPs)
	}
	if s.License != "MIT" {
		t.Fatalf("license = %q, want MIT", s.License)
	}

	const begin = "<!-- COLDCOFFEE-ACTIVATION-BEGIN -->"
	const end = "<!-- COLDCOFFEE-ACTIVATION-END -->"
	if strings.Count(s.Instructions, begin) != 1 || strings.Count(s.Instructions, end) != 1 {
		t.Fatal("activation markers must appear exactly once")
	}
	start := strings.Index(s.Instructions, begin)
	stop := strings.Index(s.Instructions, end)
	if start < 0 || stop < start {
		t.Fatal("activation markers are out of order")
	}
	block := s.Instructions[start+len(begin) : stop]
	golden, err := os.ReadFile(filepath.Join("testdata", "coldcoffee-activation.md"))
	if err != nil {
		t.Fatalf("read activation golden: %v", err)
	}
	if got, want := nonEmptyReferenceLines(block), nonEmptyReferenceLines(string(golden)); !slices.Equal(got, want) {
		t.Fatalf("activation page drifted from the golden:\n got %d lines\nwant %d lines", len(got), len(want))
	}
	// BLADE's markdown renderer has no hard line breaks: every adjacent pair of
	// non-empty page lines must be separated by a blank line.
	raw := strings.Split(block, "\n")
	for i := 1; i < len(raw); i++ {
		if strings.TrimSpace(raw[i]) != "" && strings.TrimSpace(raw[i-1]) != "" {
			t.Fatalf("activation page line %d is not separated by a blank line", i+1)
		}
	}
	for _, needle := range []string{
		"1057540028", "1077074552", "618179023", "1131239659",
		"https://t.me/coolcoffeeai", "https://t.me/chachachalkf",
		"来杯冰美式", "${SKILL_DIR}/references/normalizer.md",
		"${SKILL_DIR}/references/leaf-index.md", "${SKILL_DIR}/references/routes/leaves/",
	} {
		if !strings.Contains(s.Instructions, needle) {
			t.Fatalf("SKILL.md is missing %q", needle)
		}
	}

	refRoot := filepath.Join(root, "skills", coldCoffeeSkillName, "references")
	var referenceBytes int64
	var parents, leaves int
	err = filepath.WalkDir(refRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		referenceBytes += info.Size()
		if !strings.HasSuffix(path, ".md") {
			t.Errorf("unexpected non-markdown reference: %s", path)
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		text := string(body)
		rel, _ := filepath.Rel(refRoot, path)
		if strings.Contains(text, "{{") {
			t.Errorf("reference still contains a template placeholder: %s", rel)
		}
		if strings.Contains(text, "<!-- CHA-") {
			t.Errorf("reference still contains a seat marker: %s", rel)
		}
		if strings.HasPrefix(text, "---") {
			t.Errorf("reference still has YAML frontmatter: %s", rel)
		}
		if filepath.Dir(rel) == "routes" && strings.HasPrefix(filepath.Base(rel), "cha-") {
			parents++
		}
		if strings.HasPrefix(rel, filepath.Join("routes", "leaves")+string(filepath.Separator)) {
			leaves++
			if !strings.Contains(text, "第一步") || !strings.Contains(text, "第三步") {
				t.Errorf("leaf is missing its three-step body: %s", rel)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk references: %v", err)
	}
	if parents != 6 {
		t.Fatalf("parent route references = %d, want 6", parents)
	}
	if leaves != 80 {
		t.Fatalf("leaf route references = %d, want 80", leaves)
	}
	if len(data) > 32*1024 {
		t.Fatalf("SKILL.md is %d bytes, want <= 32 KiB", len(data))
	}
	if referenceBytes > 256*1024 {
		t.Fatalf("references total %d bytes, want <= 256 KiB", referenceBytes)
	}
}

func TestColdCoffeeCue(t *testing.T) {
	for _, key := range []string{"auto", "pentest", "mainagent"} {
		if got := coldCoffeeCue(key, []string{"api-recon", coldCoffeeSkillName}); got != coldCoffeeCueText {
			t.Fatalf("cue for %s = %q, want the activation cue", key, got)
		}
	}
	for _, tc := range []struct {
		key    string
		skills []string
	}{
		{"auto", []string{"api-recon"}},
		{"worker", []string{coldCoffeeSkillName}},
		{"planner", []string{coldCoffeeSkillName}},
		{"goals", []string{coldCoffeeSkillName}},
		{"auto", nil},
	} {
		if got := coldCoffeeCue(tc.key, tc.skills); got != "" {
			t.Fatalf("cue for %s with %v = %q, want empty", tc.key, tc.skills, got)
		}
	}
	if !strings.Contains(coldCoffeeCueText, coldCoffeeSkillName) || !strings.Contains(coldCoffeeCueText, "冷咖啡") || !strings.Contains(coldCoffeeCueText, "cold coffee") {
		t.Fatalf("cue text must name the skill and both activation words: %q", coldCoffeeCueText)
	}
}

func TestAppendColdCoffeeCue(t *testing.T) {
	visible := []string{"api-recon", coldCoffeeSkillName}
	if got := appendColdCoffeeCue("auto", "", visible); got != coldCoffeeCueText {
		t.Fatalf("empty DB append should yield the cue, got %q", got)
	}
	if got, want := appendColdCoffeeCue("pentest", "benchmark segment", visible), "benchmark segment\n\n"+coldCoffeeCueText; got != want {
		t.Fatalf("DB append should stay first, got %q", got)
	}
	if got := appendColdCoffeeCue("worker", "benchmark segment", visible); got != "benchmark segment" {
		t.Fatalf("worker must not receive the cue, got %q", got)
	}
	if got := appendColdCoffeeCue("mainagent", "db", []string{"api-recon"}); got != "db" {
		t.Fatalf("invisible skill must not receive the cue, got %q", got)
	}
}
