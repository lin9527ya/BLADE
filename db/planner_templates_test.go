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
