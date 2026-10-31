package server

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lin9527ya/blade/notify"
)

// TestMaskEnvJSONHidesCredentials 钉住 MCP env 的回显口径：疑似凭据的键必须被掩码，
// 非凭据键原样保留（UI 要靠它编辑）。
//
// 对比通知渠道走的是 notify.MaskConfig——同一类数据两套待遇，就是一次管理会话读取
// 能把全部 MCP token 导出的原因。
func TestMaskEnvJSONHidesCredentials(t *testing.T) {
	raw := json.RawMessage(`{"Authorization":"Bearer sk-real","X-API-Key":"k-1234567890","REGION":"us-east","X-Trace":"t"}`)
	var got map[string]string
	if err := json.Unmarshal(maskEnvJSON(raw), &got); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"Authorization", "X-API-Key"} {
		if !notify.IsMasked(got[k]) {
			t.Fatalf("%s 未掩码，回显值 = %q", k, got[k])
		}
		if strings.Contains(got[k], "sk-real") || strings.Contains(got[k], "k-1234567890") {
			t.Fatalf("%s 的明文仍在回显里", k)
		}
	}
	if got["REGION"] != "us-east" || got["X-Trace"] != "t" {
		t.Fatalf("非凭据键不该被改动：%v", got)
	}
}

// TestMergeMaskedEnvRestoresOriginal 钉住"掩码 = 没改"：提交回来的掩码必须换回库中原值，
// 否则一次只改 name 的保存就会把真 token 覆盖成字面量掩码。
func TestMergeMaskedEnvRestoresOriginal(t *testing.T) {
	prev := json.RawMessage(`{"Authorization":"Bearer real-token","REGION":"eu"}`)
	submitted := json.RawMessage(`{"Authorization":"` + notify.MaskedValue("Bearer real-token") + `","REGION":"us","NEW":"plain"}`)
	var got map[string]string
	if err := json.Unmarshal(mergeMaskedEnv(prev, submitted), &got); err != nil {
		t.Fatal(err)
	}
	if got["Authorization"] != "Bearer real-token" {
		t.Fatalf("掩码未还原为原值，得到 %q", got["Authorization"])
	}
	if got["REGION"] != "us" || got["NEW"] != "plain" {
		t.Fatalf("用户改动被吞掉：%v", got)
	}

	// 掩码但原值不存在：不能把掩码当凭据存进去。
	orphan := json.RawMessage(`{"Stale":"` + notify.MaskedPrefix + `:…abcdef"}`)
	var got2 map[string]string
	if err := json.Unmarshal(mergeMaskedEnv(json.RawMessage(`{}`), orphan), &got2); err != nil {
		t.Fatal(err)
	}
	if _, ok := got2["Stale"]; ok {
		t.Fatal("没有原值的掩码键应被丢弃，而不是存成凭据")
	}
}

// TestMaskToolExecMasksHTTPHeaders 钉住自定义 HTTP 工具 headers 的掩码，
// 且不碰 command/script（那是工具逻辑本身，UI 需要它才能编辑）。
func TestMaskToolExecMasksHTTPHeaders(t *testing.T) {
	raw := json.RawMessage(`{"method":"GET","url":"https://x","headers":{"Authorization":"Bearer abc","Accept":"json"},"body":"b"}`)
	var got struct {
		Method  string            `json:"method"`
		URL     string            `json:"url"`
		Headers map[string]string `json:"headers"`
		Body    string            `json:"body"`
	}
	if err := json.Unmarshal(maskToolExecForUI("http", raw), &got); err != nil {
		t.Fatal(err)
	}
	if !notify.IsMasked(got.Headers["Authorization"]) || got.Headers["Accept"] != "json" {
		t.Fatalf("headers 掩码不对：%v", got.Headers)
	}
	if got.URL != "https://x" || got.Method != "GET" || got.Body != "b" {
		t.Fatalf("非 headers 字段被改动：%+v", got)
	}
	// 命令类工具原样返回。
	cmd := json.RawMessage(`{"command":"echo hi"}`)
	if string(maskToolExecForUI("command", cmd)) != string(cmd) {
		t.Fatal("command 类 exec 不该被掩码")
	}
}
