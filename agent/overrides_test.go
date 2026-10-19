package agent

import (
	"context"
	"testing"

	actool "github.com/lin9527ya/Norma/tool"
)

// TestWithOverridesRestoresOnExitAndPanic pins the scoping contract: whatever the
// previous hook set was, it must be back after fn returns — and after fn panics
// (a leaked override would silently change every later test in the package).
func TestWithOverridesRestoresOnExitAndPanic(t *testing.T) {
	base := Overrides{ArtifactLanguage: func() string { return "zh" }}
	SetOverrides(base)
	t.Cleanup(func() { SetOverrides(Overrides{}) })

	WithOverrides(Overrides{ArtifactLanguage: func() string { return "en" }}, func() {
		if got := ArtifactLanguage(); got != "en" {
			t.Fatalf("inside WithOverrides: got %q, want en", got)
		}
	})
	if got := ArtifactLanguage(); got != "zh" {
		t.Fatalf("after WithOverrides: got %q, want the previous zh", got)
	}

	func() {
		defer func() { _ = recover() }()
		WithOverrides(Overrides{ArtifactLanguage: func() string { return "en" }}, func() {
			panic("boom")
		})
	}()
	if got := ArtifactLanguage(); got != "zh" {
		t.Fatalf("after a panicking WithOverrides: got %q, want the previous zh", got)
	}
}

// TestSetOverridesReplacesEveryHook guards against a field being added to Overrides
// but forgotten in SetOverrides/CurrentOverrides (the two lists must stay in sync).
func TestSetOverridesReplacesEveryHook(t *testing.T) {
	all := Overrides{
		PromptOverride:                 func(string) (string, bool) { return "p", true },
		CompactionPromptOverride:       func() (string, bool) { return "c", true },
		ArtifactLanguage:               func() string { return "a" },
		WrapupOverride:                 func(string) (string, bool) { return "w", true },
		WrapupMaxTurnsOverride:         func(string) (int, bool) { return 1, true },
		WrapupTaskTimeoutOverride:      func(string) (string, bool) { return "t", true },
		WrapupTaskTimeoutTurnsOverride: func(string) (int, bool) { return 2, true },
		FindingTrafficBindingEnabled:   func() bool { return true },
		ToolResolve: func(context.Context, string, []actool.CoreTool) []actool.CoreTool {
			return nil
		},
		ToolAugment: func(context.Context, string) ([]actool.CoreTool, DeferredInfo, func()) {
			return nil, DeferredInfo{}, nil
		},
	}
	SetOverrides(all)
	t.Cleanup(func() { SetOverrides(Overrides{}) })

	got := CurrentOverrides()
	if got.PromptOverride == nil || got.CompactionPromptOverride == nil ||
		got.ArtifactLanguage == nil || got.WrapupOverride == nil ||
		got.WrapupMaxTurnsOverride == nil || got.WrapupTaskTimeoutOverride == nil ||
		got.WrapupTaskTimeoutTurnsOverride == nil || got.FindingTrafficBindingEnabled == nil ||
		got.ToolResolve == nil || got.ToolAugment == nil {
		t.Fatalf("CurrentOverrides 丢了字段：%+v", got)
	}

	// 整体替换语义：nil 字段必须把那一项清掉，而不是保留旧值。
	SetOverrides(Overrides{})
	if CurrentOverrides().PromptOverride != nil {
		t.Fatal("SetOverrides 应整体替换；nil 字段没有清掉旧 hook")
	}
}
