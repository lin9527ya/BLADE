package agent

import (
	"fmt"
	"strings"
	"testing"

	"github.com/lin9527ya/blade/db"
)

func TestSimilarFinding(t *testing.T) {
	prior := []db.SimilarFinding{
		{NodeID: 11, Summary: "用户 id 参数存在 SQL 注入，可读取任意数据", AssetIDs: []int64{7}},
		{NodeID: 12, Summary: "登录处存在反射型 XSS", AssetIDs: []int64{8}},
	}
	if id, _ := similarFinding("用户 id 参数存在SQL注入，可读取任意数据！", []int64{7}, prior); id != 11 {
		t.Fatalf("同资产同摘要（标点/空格差异）应判重复，got %d", id)
	}
	if id, _ := similarFinding("用户 id 参数存在 SQL 注入，可读取任意数据", []int64{99}, prior); id != 0 {
		t.Fatal("资产不同不应判重复")
	}
	if id, _ := similarFinding("完全不同的漏洞：命令执行", []int64{7}, prior); id != 0 {
		t.Fatal("摘要不同不应判重复")
	}
	if id, _ := similarFinding("短", []int64{7}, prior); id != 0 {
		t.Fatal("过短摘要没有判别力，不应判重复")
	}
}

// 重复 report_finding 应被拒收并指回已有条目，而不是造出两条同名漏洞。
func TestReportFindingRejectsDuplicate(t *testing.T) {
	tools, _, intent := newToolWorker(t)
	body := fmt.Sprintf(`{"vulnclass":"sqli","severity":"medium","summary":"用户 id 参数存在 SQL 注入，可读取任意数据","confidence":"medium","intent_id":%d}`, intent)
	out := callTool(t, tools.WorkerTools(), "report_finding", body)
	if !strings.Contains(out, "finding recorded") {
		t.Fatalf("首次登记应成功：%s", out)
	}
	out = callTool(t, tools.WorkerTools(), "report_finding", body)
	if !strings.Contains(out, "已存在高度相似的漏洞") || !strings.Contains(out, "merge_findings") {
		t.Fatalf("重复登记应被拒并指向合并工具：%s", out)
	}
}
