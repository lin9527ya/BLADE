package agent

import (
	"strings"
	"testing"
)

// 出站产物语言（P41）：默认**不加一个字**（不约束），切到 en 才把规约段拼进系统提示词。
// 这一刀最容易出的错是"悄悄多了一段约束"——所以先钉住"默认时提示词一个字符都没变"。

func TestArtifactLanguageBlockOnlyWhenEn(t *testing.T) {
	old := ArtifactLanguage
	t.Cleanup(func() { ArtifactLanguage = old })

	ArtifactLanguage = nil
	if got := artifactLanguageBlock(); got != "" {
		t.Errorf("没有接线时该什么都不加：%q", got)
	}
	for _, lang := range []string{"", "zh", "ZH", "  ", "jp"} {
		ArtifactLanguage = func() string { return lang }
		if got := artifactLanguageBlock(); got != "" {
			t.Errorf("语言=%q 该不加任何东西（默认档=不约束），得到 %.40q…", lang, got)
		}
	}

	ArtifactLanguage = func() string { return ArtifactLangEn }
	got := artifactLanguageBlock()
	if got == "" {
		t.Fatal("en 该给出规约段")
	}
	// 三件必须说到的事：约束对象是**出站产物**、要求是 ASCII 英文、业务需要时留门并留痕。
	for _, want := range []string{"ASCII", "出站产物", "业务确需", "意图或 fact"} {
		if !strings.Contains(got, want) {
			t.Errorf("规约段该点明 %q：\n%s", want, got)
		}
	}
}

func TestDeferredSystemCarriesArtifactLanguage(t *testing.T) {
	old := ArtifactLanguage
	t.Cleanup(func() { ArtifactLanguage = old })

	// zh：系统提示词与"没有这个功能"完全一致（一个字符都不多）。
	ArtifactLanguage = func() string { return ArtifactLangZh }
	zh, _ := deferredSystem("BASE", DeferredInfo{})
	if len(zh) != 1 || zh[0] != "BASE" {
		t.Fatalf("zh 档不该改动系统提示词：%#v", zh)
	}

	// en：规约段随稳定段一起下发（四个 agent——planner/worker/mainagent/chat——都从
	// deferredSystem 出 system 段，所以这一处覆盖全部）。
	ArtifactLanguage = func() string { return ArtifactLangEn }
	en, _ := deferredSystem("BASE", DeferredInfo{})
	if len(en) == 0 || !strings.Contains(en[0], "面向目标的产物语言") {
		t.Fatalf("en 档该把规约拼进系统提示词：%#v", en)
	}
	if !strings.HasPrefix(en[0], "BASE") {
		t.Errorf("规约该追加在原提示词之后，而不是替换它：%.60q", en[0])
	}
}
