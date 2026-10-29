package db

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestPlannerTemplateStorage(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	t.Cleanup(func() { d.Close() })

	stamp := time.Now().UnixNano()
	name := fmt.Sprintf("tpl-%d", stamp)
	created, err := d.CreatePlannerTemplate(name, "正文 {{.Goal}}")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM planner_templates WHERE id=$1`, created.ID) })

	if _, err := d.CreatePlannerTemplate(name, "重复"); !errors.Is(err, ErrPlannerTemplateNameConflict) {
		t.Fatalf("duplicate name error = %v", err)
	}
	if _, err := d.CreatePlannerTemplate("bad name!", "x"); !errors.Is(err, ErrPlannerTemplateInvalid) {
		t.Fatalf("invalid name error = %v", err)
	}
	if _, err := d.CreatePlannerTemplate("empty", strings.Repeat("x", MaxPlannerTemplateBodyBytes+1)); !errors.Is(err, ErrPlannerTemplateInvalid) {
		t.Fatalf("oversized body error = %v", err)
	}

	if _, err := d.SetDefaultPlannerTemplate(created.ID); err != nil {
		t.Fatal(err)
	}
	body, err := d.DefaultPlannerTemplateBody()
	if err != nil || body != "正文 {{.Goal}}" {
		t.Fatalf("default body = %q err=%v", body, err)
	}
	if err := d.DeletePlannerTemplate(created.ID); !errors.Is(err, ErrPlannerTemplateDefaultLocked) {
		t.Fatalf("default delete error = %v", err)
	}

	second, err := d.CreatePlannerTemplate(fmt.Sprintf("tpl2-%d", stamp), "第二份")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM planner_templates WHERE id=$1`, second.ID) })
	if _, err := d.SetDefaultPlannerTemplate(second.ID); err != nil {
		t.Fatal(err)
	}
	first, err := d.GetPlannerTemplate(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if first.IsDefault {
		t.Fatal("setting a new default must clear the previous one")
	}
	if err := d.DeletePlannerTemplate(created.ID); err != nil {
		t.Fatalf("non-default delete: %v", err)
	}
}

// 内置领域模板用中文名（内网渗透/权限提升/…），nkey 做恒等映射。这条钉住两件事：
// 中文名能建、能按 nkey 查回；空白与控制字符仍然被拒。
func TestPlannerTemplateChineseName(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	t.Cleanup(func() { d.Close() })

	stamp := time.Now().UnixNano()
	name := fmt.Sprintf("内网渗透-%d", stamp)
	created, err := d.CreatePlannerTemplate(name, "正文 {{.Goal}}")
	if err != nil {
		t.Fatalf("中文名应被接受: %v", err)
	}
	t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM planner_templates WHERE id=$1`, created.ID) })
	if created.NKey != strings.ToLower(name) {
		t.Fatalf("nkey 应为小写恒等映射，得到 %q", created.NKey)
	}
	got, err := d.GetPlannerTemplateByNKey(created.NKey)
	if err != nil || got == nil || got.ID != created.ID {
		t.Fatalf("按 nkey 查不回: %v %+v", err, got)
	}

	// 名称里的空白在 normalize 阶段被折叠（"内网 渗透" → "内网-渗透"），这是与
	// task_categories 一致的口径；真正该拒的是标点与控制字符。
	for _, bad := range []string{"内网.渗透", "内网/渗透", "内网:渗透", "bad name!", "a\\b"} {
		if _, err := d.CreatePlannerTemplate(bad, "x"); !errors.Is(err, ErrPlannerTemplateInvalid) {
			t.Fatalf("名称 %q 应被拒，得到 %v", bad, err)
		}
	}
}

// 播种幂等：首插入返回 true，已存在（含用户自建同名）返回 false 且不覆盖正文。
func TestSeedPlannerTemplateIfEmpty(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	t.Cleanup(func() { d.Close() })

	name := fmt.Sprintf("seed-%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM planner_templates WHERE nkey=$1`, strings.ToLower(name)) })

	inserted, err := d.SeedPlannerTemplateIfEmpty(name, "首版 {{.Goal}}", false)
	if err != nil || !inserted {
		t.Fatalf("首插应成功: inserted=%v err=%v", inserted, err)
	}
	inserted, err = d.SeedPlannerTemplateIfEmpty(name, "改过的正文", false)
	if err != nil || inserted {
		t.Fatalf("重复播种应 no-op: inserted=%v err=%v", inserted, err)
	}
	got, err := d.GetPlannerTemplateByNKey(strings.ToLower(name))
	if err != nil || got == nil {
		t.Fatal(err)
	}
	if got.Body != "首版 {{.Goal}}" || !got.Builtin {
		t.Fatalf("播种不应覆盖已有正文: %+v", got)
	}

	// 用户自建同名（builtin=false）也不该被改写成内置。
	userName := fmt.Sprintf("user-%d", time.Now().UnixNano())
	user, err := d.CreatePlannerTemplate(userName, "用户正文")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM planner_templates WHERE id=$1`, user.ID) })
	if inserted, err := d.SeedPlannerTemplateIfEmpty(userName, "内置正文", false); err != nil || inserted {
		t.Fatalf("同名用户模板应阻止播种: inserted=%v err=%v", inserted, err)
	}
	if got, _ := d.GetPlannerTemplate(user.ID); got == nil || got.Body != "用户正文" || got.Builtin {
		t.Fatalf("用户模板被覆盖: %+v", got)
	}
}
