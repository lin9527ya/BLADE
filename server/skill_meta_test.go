package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// putSkillMeta 直接调 fsUpdateSkillMeta（照 skill_upload_test.go 的 harness 写法）。
func putSkillMeta(t *testing.T, skillDir, name, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/api/skills/"+name+"/meta", strings.NewReader(body))
	req.SetPathValue("name", name)
	rr := httptest.NewRecorder()
	(&Server{skillDir: skillDir}).fsUpdateSkillMeta(rr, req)
	return rr
}

// TestUpdateSkillMetaRefusesMalformedFrontmatter 是这次事故的核心回归：
// 前导块不合法的 SKILL.md，元数据接口必须**拒绝且一个字节都不改**。此前它会把这
// 段按 key: value 重排——空行没了、JSON 缩进没了、`http://<主机>` 被第一个冒号
// 拆成 `http: //<主机>`，一次正常的「关联 MCP」操作就把技能文件改坏。
func TestUpdateSkillMetaRefusesMalformedFrontmatter(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "broken"), 0o755); err != nil {
		t.Fatal(err)
	}
	md := "---\n" +
		"## name: broken\n" +
		"description: d\n" +
		"# 标题\n" +
		"正文句子。\n" +
		"- 默认 Web 界面：`http: //<主机>`\n" +
		"---\n" +
		"正文\n"
	p := filepath.Join(dir, "broken", "SKILL.md")
	if err := os.WriteFile(p, []byte(md), 0o644); err != nil {
		t.Fatal(err)
	}

	rr := putSkillMeta(t, dir, "broken", `{"mcps":["browser"]}`)
	if rr.Code != 400 {
		t.Fatalf("畸形 frontmatter 应被拒绝，实际 status=%d body=%s", rr.Code, rr.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &out)
	msg, _ := out["error"].(string)
	if !strings.Contains(msg, "没有改动文件") || !strings.Contains(msg, "frontmatter") {
		t.Fatalf("400 文案要说清原因与「文件没动」：%s", rr.Body.String())
	}
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, []byte(md)) {
		t.Fatalf("被拒绝的请求不该改动文件，实际变成：\n%s", got)
	}
}

// TestUpdateSkillMetaUpdatesOnlyFrontmatter 是好路径的对照：规范文件照常改元数据，
// 且**正文逐字节保留**（写入器的唯一职责就是不碰正文）。
func TestUpdateSkillMetaUpdatesOnlyFrontmatter(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "good"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "# 标题\n\n正文第一行。\n\n## 小节\n\n- 项\n"
	md := "---\nname: good\ndescription: 原始描述\n---\n" + body
	p := filepath.Join(dir, "good", "SKILL.md")
	if err := os.WriteFile(p, []byte(md), 0o644); err != nil {
		t.Fatal(err)
	}

	rr := putSkillMeta(t, dir, "good", `{"mcps":["browser","filesystem"],"description":"新描述"}`)
	if rr.Code != 200 {
		t.Fatalf("规范文件应改成功，实际 status=%d body=%s", rr.Code, rr.Body.String())
	}
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)
	if !strings.Contains(s, "description: 新描述") {
		t.Fatalf("description 应更新：\n%s", s)
	}
	if !strings.Contains(s, "mcps: browser, filesystem") {
		t.Fatalf("mcps 应按单行逗号分隔写回：\n%s", s)
	}
	if !strings.HasSuffix(s, body) {
		t.Fatalf("正文必须逐字节保留：\n%s", s)
	}
}

// TestListSkillsFlagsFrontmatter 钉住"清单里如实提示"：畸形文件带上 meta_error、
// 缺字段带上 meta_warning、健康文件两个都不带。
func TestListSkillsFlagsFrontmatter(t *testing.T) {
	dir := t.TempDir()
	writeSkill := func(name, md string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name, "SKILL.md"), []byte(md), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeSkill("good", "---\nname: good\ndescription: d\n---\n正文\n")
	writeSkill("broken", "---\n## name: broken\n描述句子。\n---\n正文\n")
	writeSkill("thin", "---\nlicense: MIT\n---\n正文\n")

	rr := httptest.NewRecorder()
	(&Server{m: &Manager{}, skillDir: dir}).fsListSkills(rr, httptest.NewRequest(http.MethodGet, "/api/skills", nil))
	if rr.Code != 200 {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var out struct {
		Skills []skillFileNode `json:"skills"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("列表不是合法 JSON: %v", err)
	}
	byName := map[string]skillFileNode{}
	for _, n := range out.Skills {
		byName[n.Name] = n
	}
	if len(byName) != 3 {
		t.Fatalf("应有 3 个技能，实际 %d：%+v", len(byName), byName)
	}
	if g := byName["good"]; g.MetaError != "" || g.MetaWarning != "" {
		t.Fatalf("健康文件不该有提示：%+v", g)
	}
	if b := byName["broken"]; !strings.Contains(b.MetaError, "frontmatter") || b.MetaWarning != "" {
		t.Fatalf("畸形文件应带 meta_error（且不叠软警告）：%+v", b)
	}
	if th := byName["thin"]; th.MetaError != "" || !strings.Contains(th.MetaWarning, "缺 name") {
		t.Fatalf("缺字段应带 meta_warning：%+v", th)
	}
}
