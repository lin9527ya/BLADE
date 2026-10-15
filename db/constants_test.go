package db

import (
	"encoding/json"
	"reflect"
	"testing"
)

// ATT&CK 技术 ID 的归一化：只查形状、不查真伪；非法项丢弃而不是拒绝整条意图。
//
// 「不查真伪」是个明确的取舍：查真伪要内置一份会过期的 ATT&CK 表，而形状合法但其实是错的 ID
// 读者一眼能看穿（ID 是行内通用语）；放任自由文本则连"这是个 ID"都保证不了。
func TestNormalizeTechniques(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"小写与空白归一", []string{" t1190 ", "T1552.001"}, []string{"T1190", "T1552.001"}},
		{"去重", []string{"T1190", "t1190"}, []string{"T1190"}},
		{"丢非形状项", []string{"T1190", "凭据访问", "T1", "T12345", ""}, []string{"T1190"}},
		{"保留书写顺序", []string{"T1021", "T1190", "T1003"}, []string{"T1021", "T1190", "T1003"}},
		{"限长", []string{"T1001", "T1002", "T1003", "T1004", "T1005", "T1006", "T1007", "T1008", "T1009"},
			[]string{"T1001", "T1002", "T1003", "T1004", "T1005", "T1006", "T1007", "T1008"}},
		{"空进空出", nil, []string{}},
	}
	for _, c := range cases {
		got := NormalizeTechniques(c.in)
		if len(got) == 0 && len(c.want) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

// 技术统计：软删除的意图不计（用户主动拿掉了），其余都计——被 blocked 的也算「试过」，
// 覆盖率答的是「打过哪些面」，不是「哪些面打赢了」。
func TestTechniqueTally(t *testing.T) {
	mk := func(id int64, state string, techs ...string) *Node {
		p, _ := json.Marshal(map[string]any{"summary": "x", "techniques": techs})
		return &Node{ID: id, Kind: KindIntent, State: state, Payload: p}
	}
	tally := TechniqueTally([]*Node{
		mk(1, "done", "T1190"),
		mk(2, "blocked", "T1190"),          // 试过就算
		mk(3, StateIntentDeleted, "T1190"), // 用户拿掉了 → 不算
		mk(4, "running", "T1552.001"),
		mk(5, "done"), // 没标注
	})
	want := map[string]int{"T1190": 2, "T1552.001": 1}
	if !reflect.DeepEqual(tally, want) {
		t.Fatalf("got %v want %v", tally, want)
	}
}
