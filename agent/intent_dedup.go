package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"

	"github.com/lin9527ya/blade/db"
)

// 防打转硬防护：同一批资产、同一风险类、描述几乎一样的意图，重复派多少次都不会
// 得到不同结果，只是在烧 token。
//
// **只挡措辞几乎相同的重复**。压缩之后 planner 看不到旧方向、把同一句话又写一遍，
// 是这里要拦的典型场景；机理不同、措辞不同的方向一律放行——发散性是本系统的主要
// 目标，宁可漏挡，不可错挡。所以判定是三条同时成立：同资产集、同风险类、描述高度重合。
const (
	intentDupSimilarity = 0.65 // 描述相似度（字符二元组 Jaccard）
	intentDupAssetJacc  = 0.5  // 资产集合重合度
	intentDupAttemptCap = 3    // 同方向已结束的尝试达到这个数：必须换打法
	intentPoolScanCap   = 300  // 只比最近的这些意图（更早的与当前决策无关）
)

// intentFP 是一条已有意图的对比指纹（只保留去重需要的字段）。
type intentFP struct {
	id       int64
	summary  string
	riskArea string
	state    string
	assets   map[int64]bool
}

// loadIntentPool 读最近的意图做对比基准（一次 add_intent 调用只读一次）。
func (t *ToolSet) loadIntentPool() []intentFP {
	nodes, err := t.ts.ListByKind(db.KindIntent, intentPoolScanCap)
	if err != nil {
		return nil
	}
	out := make([]intentFP, 0, len(nodes))
	for _, n := range nodes {
		var p map[string]any
		_ = json.Unmarshal(n.Payload, &p)
		out = append(out, intentFP{
			id: n.ID, summary: asText(p["summary"]), state: n.State,
			riskArea: db.NormalizeRiskArea(asText(p["risk_area"])),
			assets:   idSetOf(p["asset_ids"]),
		})
	}
	return out
}

// poolFor 惰性加载并缓存对比池：一次 add_intent 可能带 4 条意图，不该读 4 次库。
func (q *intentQuota) poolFor(t *ToolSet) []intentFP {
	if !q.poolLoaded {
		q.pool = t.loadIntentPool()
		q.poolLoaded = true
	}
	return q.pool
}

// rememberIntent 把刚建的意图放进对比池，让同一批里的重复也能被当场拦住。
func (q *intentQuota) rememberIntent(fp intentFP) {
	if !q.poolLoaded {
		return // 没加载过：下次要判定时整体加载，自然会包含这条
	}
	q.pool = append(q.pool, fp)
}

// checkIntentSpin 返回 "" 表示放行；否则是一条给模型看的拒绝理由。
//
// 两道判定：
//  1. 已有 **open/running/paused** 的意图几乎是同一条方向 → 不重复派活（派出去
//     也是两条 worker 干一件事）。
//  2. 同一方向已经**结束**过 3 次（done/negative/exhausted/blocked）→ 同样的打法
//     再来一次不会不同，必须换机理/入口/资产。
func (t *ToolSet) checkIntentSpin(q *intentQuota, summary, riskArea string, assets []int64, explicitRisk bool) string {
	if t.ts == nil || strings.TrimSpace(summary) == "" {
		return ""
	}
	// 既没绑资产也没填风险类：只剩文字相似，没有结构性锚点可比。放过——两个都
	// 没说清的新想法不该被当成重复（发散优先于整齐）。
	if len(assets) == 0 && !explicitRisk {
		return ""
	}
	newAssets := make(map[int64]bool, len(assets))
	for _, a := range assets {
		newAssets[a] = true
	}
	var active *intentFP
	attempts := make([]intentFP, 0, intentDupAttemptCap)
	pool := q.poolFor(t)
	for i := range pool {
		fp := &pool[i]
		if fp.state == "deleted" || !assetsComparable(newAssets, fp.assets) || fp.riskArea != riskArea {
			continue
		}
		if summarySimilarity(summary, fp.summary) < intentDupSimilarity {
			continue
		}
		switch fp.state {
		case "open", "running", "paused":
			if active == nil {
				active = fp
			}
		case "done", "negative", "out-of-scope", "exhausted", "blocked", "no_output":
			attempts = append(attempts, *fp)
		}
	}
	// 叙事里的 intent-N 用**任务内序号**（全局 id 跨任务连续，读起来像漏了几百条）；
	// 但要调工具的那处必须给出可寻址的全局 id。
	seqOf := func(id int64) int64 { return id }
	if seq, err := t.ts.IntentOrdinals(0); err == nil && len(seq) > 0 {
		seqOf = func(id int64) int64 {
			if v := seq[id]; v > 0 {
				return v
			}
			return id
		}
	}
	if active != nil {
		return fmt.Sprintf("未创建：与仍在进行的 intent-%d「%s」几乎是同一条方向（同一批资产、同一风险类，描述高度重合）。"+
			"这不是策略拒绝、也不是目标侧防御——只是避免两条 worker 干同一件事。"+
			"若这确实是**不同的打法**，请在 summary 里写清与它的机理差异（打哪个参数、用什么载荷、验证哪个假设）后重提；"+
			"若是同一件事，去看它（get_worker_output / get_worker_trace，intent_id=%d）的进度，别重复派。",
			seqOf(active.id), clipLine(active.summary, 60), active.id)
	}
	if len(attempts) >= intentDupAttemptCap {
		ids := make([]string, 0, len(attempts))
		for _, a := range attempts {
			ids = append(ids, fmt.Sprintf("intent-%d(%s)", seqOf(a.id), a.state))
		}
		return fmt.Sprintf("未创建：这个方向已经按几乎相同的描述尝试过 %d 次（%s）。"+
			"同一打法重复第四次不会得到不同结果——换机理、换入口或换资产。"+
			"若这条路确实走不通，先用 record_fact 把否定结论写下来（试到哪、缺什么条件），再派下一条。",
			len(attempts), strings.Join(ids, "、"))
	}
	return ""
}

// assetsComparable 判断两批资产是不是"同一批"：两边都空算同一批（此时靠风险类与
// 描述判定），一边空一边不空不算，否则看 Jaccard 重合度。
func assetsComparable(a, b map[int64]bool) bool {
	if len(a) == 0 || len(b) == 0 {
		return len(a) == 0 && len(b) == 0
	}
	inter := 0
	for id := range a {
		if b[id] {
			inter++
		}
	}
	if inter == 0 {
		return false
	}
	return float64(inter)/float64(len(a)+len(b)-inter) >= intentDupAssetJacc
}

// summarySimilarity 是字符二元组的 Jaccard 相似度。中文没有空格分词，按字切
// 二元组对改写/换序最稳；标点与空白先去掉（"。"、"/"、"：" 这些差别不该算作新方向）。
// 过短的描述不参与判定（样本太少，相似度没有意义）。
func summarySimilarity(a, b string) float64 {
	sa, sb := shingles(a), shingles(b)
	if len(sa) == 0 || len(sb) == 0 {
		return 0
	}
	inter := 0
	for k := range sa {
		if _, ok := sb[k]; ok {
			inter++
		}
	}
	return float64(inter) / float64(len(sa)+len(sb)-inter)
}

const minShingleRunes = 6 // 少于这个字数的描述不判定相似

func shingles(s string) map[string]struct{} {
	rs := make([]rune, 0, len(s))
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			rs = append(rs, r)
		}
	}
	if len(rs) < minShingleRunes {
		return nil
	}
	set := make(map[string]struct{}, len(rs))
	for i := 0; i+1 < len(rs); i++ {
		set[string(rs[i:i+2])] = struct{}{}
	}
	return set
}

// idSetOf 把 payload 里的 id 数组（JSON 反序列化后是 []any/float64）转成集合。
func idSetOf(v any) map[int64]bool {
	list, ok := v.([]any)
	if !ok || len(list) == 0 {
		return nil
	}
	out := make(map[int64]bool, len(list))
	for _, item := range list {
		switch n := item.(type) {
		case float64:
			out[int64(n)] = true
		case int64:
			out[n] = true
		case json.Number:
			if i, err := n.Int64(); err == nil {
				out[i] = true
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
