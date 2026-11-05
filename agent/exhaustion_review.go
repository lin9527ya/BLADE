package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lin9527ya/blade/db"
)

// 穷尽复核（exhaustion review）：planner 声称"策略内已无可行方向"时，由一个**独立的
// 只读裁判**按证据复核这个判断，而不是直接暂停任务。
//
// 为什么需要它：`mark_strategy_exhausted` 的三道硬门只能数「有没有碰过、碰了几类风险」，
// 数不了「这条证据是否真的排除了这个方向」。实测里最常见的误判是：一条意图被记为
// negative / no_output（工具失败、超时、被 WAF 拦、只试了一种 payload），planner 把它
// 当成"这条路走不通"，然后据此宣告穷尽。硬门放行（资产都碰过了、风险类都记过），
// 于是任务在很浅的位置停住。
//
// 裁判的职责被刻意限定得很窄：**只判"已给出的证据是否足以支撑这个方向的结论"**，
// 不要求它想出新方向、不要求它重新规划。这样即使裁判模型能力一般，也能可靠地发现
// "拿一个失败的工具调用当否定结论"这一类问题。

// ExhaustionClaim 是一次穷尽声明，连同复核所需的证据。
type ExhaustionClaim struct {
	Reason string
	// Directions 是逐方向的结论：方向描述 + 支撑它的证据摘要 + 终态。
	Directions []ExhaustionDirection
	// Coverage 是范围面的客观数字（给裁判判断"是不是真的走完了"）。
	Coverage ExhaustionCoverage
}

// ExhaustionDirection 是一个"已被排除"的方向及其依据。
type ExhaustionDirection struct {
	Summary string `json:"summary"`
	// Outcome 是意图的终态（done/negative/blocked/no_output/exhausted/...）。
	Outcome string `json:"outcome"`
	// Evidence 是支撑该结论的事实/发现摘要（可能为空——正是裁判要挑的毛病）。
	Evidence string `json:"evidence,omitempty"`
}

// ExhaustionCoverage 是范围面的客观计数。
type ExhaustionCoverage struct {
	InScopeAssets  int `json:"in_scope_assets"`
	UntouchedSilos int `json:"untouched_assets"`
	ShallowAssets  int `json:"shallow_assets"`
	DepthThreshold int `json:"depth_threshold"`
	// Depth 是「资产 × 风险类」的投入面：几条意图、产出几条事实。三道硬门只能数
	// "碰没碰过"，数不了"是不是每类只取了一次样"——这正是复核员要看的深度证据。
	Depth []ExhaustionDepthRow `json:"depth,omitempty"`
	// LedgerFailed / LedgerWaived 来自探索台账：failed = 测过但没结论的格子（未豁免），
	// 它和"没碰过"不是一回事——失败只说明这次没测成，不说明这条路不通。
	LedgerFailed int `json:"ledger_failed"`
	LedgerWaived int `json:"ledger_waived"`
	// ChainsOpen 是"从没被推进/上次没善终"的据点与凭据数——深度面的客观信号。
	ChainsOpen int `json:"chains_open"`
}

// ExhaustionDepthRow 是一行投入面：某资产某风险类的意图数与事实数。
type ExhaustionDepthRow struct {
	Asset    string `json:"asset"`
	RiskArea string `json:"risk_area"`
	Intents  int    `json:"intents"`
	Facts    int    `json:"facts"`
}

// intentDepthEntry 是单条意图对深度投入面的贡献。
type intentDepthEntry struct {
	area  string
	facts int
}

// ExhaustionVerdict 是复核结论。
type ExhaustionVerdict struct {
	// Accurate=true 表示证据足以支撑"确实穷尽"，任务可以按原计划暂停。
	Accurate bool `json:"accurate"`
	// Reason 是给人看的一句话结论（进时间线）。
	Reason string `json:"reason"`
	// Gaps 是裁判认为"结论下早了"的具体点，每条都要能对应到一个方向或一项缺口。
	Gaps []string `json:"gaps,omitempty"`
}

// ParseExhaustionVerdict 严格解析复核员的输出。
//
// 失败即返回 error，由调用方决定失败策略——**默认按"不准确"处理**（见 server 侧）：
// 复核员挂了就放行穷尽，等于这个看门狗在最需要它的时候恰好不存在。
func ParseExhaustionVerdict(text string) (ExhaustionVerdict, error) {
	var out ExhaustionVerdict
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start < 0 || end <= start {
		return out, fmt.Errorf("复核结论不是 JSON 对象：%s", firstLine(text, 200))
	}
	if err := json.Unmarshal([]byte(text[start:end+1]), &out); err != nil {
		return out, fmt.Errorf("复核结论解析失败：%w", err)
	}
	if strings.TrimSpace(out.Reason) == "" {
		return out, fmt.Errorf("复核结论缺少 reason")
	}
	return out, nil
}

// exhaustionReviewPrompt 是复核员的系统提示。
//
// 刻意把「什么算证据不足」写成可核对的清单，而不是让模型自由发挥：这份提示的产出会
// 直接决定任务停不停，模糊的标准会让它变成"看心情放行"。
// ExhaustionReviewPrompt 是复核员的系统提示（供 server 侧注入 LLM 调用）。
const ExhaustionReviewPrompt = `你是渗透测试平台的**穷尽复核员**。规划者声称"授权范围内已无可行方向"，请你只做一件事：判断它给出的证据**是否足以支撑这个结论**。

你不负责想新方向、不负责重新规划，只判断已有证据够不够。

## 判定为「证据不足」（accurate=false）的典型情形

1. **用失败当否定**：方向的依据是工具失败/超时/被 WAF 拦/连接失败/模型报错（blocked、no_output、exhausted 终态），而不是"目标明确拒绝了"的观察。失败只说明"这次没测成"，不说明"这条路不通"。
2. **只试了一种变体**：声称某类注入/越权/上传被挡住，但证据里只出现一个 payload、一个参数、一个编码。绕过变体（不同编码、不同参数位置、大小写、分块）没有体现。
3. **证据缺失或对不上**：方向描述说"已证否"，但没有任何具体观察（响应码、错误信息、行为差异）支撑；或证据描述的是另一件事。
4. **范围面没走完**：给出的 untouched_assets > 0，或 shallow_assets > 0（还有资产只测了很少几类风险）。
5. **把"需要凭据/需要后台"当成否定**：因未登录而测不了，属于**外部阻断**，不是"该方向已排除"。这类方向应标为 blocked 而不是 negative。
6. **单次取样**：给出的 depth（资产 × 风险类 → 意图数 / 事实数）显示每个方向都只有 1 条意图、1 条事实，而 reason 却声称"该类已排除"。一次观察只能证明"这次没成"，排除一类问题需要覆盖它的主要变体（编码、参数位置、方法、路径变体、认证前提）。
7. **失败格未处理**：ledger_failed > 0 表示台账里还有「测过但没结论」的格子（blocked/exhausted/no_output）且没有豁免。失败不是否定——这些格子要先换角度重派，或由规划者写明接受的代价，才谈得上穷尽。
8. **通路没推进**：chains_open > 0 表示还有据点/凭据从没被任何意图动用，或动过但没产出且最后一次没善终。手上还握着能继续往里推的访问却宣称穷尽，通常意味着深度没走完（提权/横向/凭据验证），需要给出为什么停在原地的证据。

## 判定为「证据充分」（accurate=true）的情形

- 每个被排除的方向都有**具体观察**（响应内容、状态码变化、行为差异、组件版本与已知修复状态），且覆盖了该类问题的主要变体；
- 或者方向确实被授权边界/操作约束挡住，并写明是哪一条；
- 且范围面确实走完（untouched_assets=0 且 shallow_assets=0，或已说明剩余资产的不可达/越界原因）。

## 输出

只输出一个 JSON 对象，不要任何其他文字：

{"accurate": true|false, "reason": "一句话结论", "gaps": ["具体的不足点，每条对应一个方向或一项缺口"]}

判 accurate=true 时 gaps 可为空数组。判断标准从严：**宁可让任务多跑一轮，也不要在证据不足时停掉它**——停掉之后没人会再推进，而多跑一轮的代价只是几次工具调用。`

// BuildExhaustionClaim 组装一次穷尽声明所需的证据：planner 的原话 + 图里已终结意图
// （方向 / 终态 / 产出证据）+ 范围面的客观计数。
//
// 证据取自**图**而不是 planner 的自述：planner 的 reason 是它的结论，而复核员要核对的
// 恰恰是结论与图对不对得上。图里的终态与产出是它绕不过去的事实。
func BuildExhaustionClaim(ts *db.ExplorationStore, as *db.AssetStore, taskID int64, reason string) ExhaustionClaim {
	claim := ExhaustionClaim{Reason: reason}
	if ts == nil {
		return claim
	}
	// 方向证据：最近一批已终结意图。只看最近 40 条——复核员判断的是"这些结论站不站得住"，
	// 不是重建整段历史；更早的意图即使有毛病，也不影响"现在该不该停"。
	depthIntents := map[int64]intentDepthEntry{}
	var depthIDs []int64
	intents, err := ts.ListByKind(db.KindIntent, 200)
	if err == nil {
		kept := 0
		for _, n := range intents {
			if kept >= 40 {
				break
			}
			switch n.State {
			case "done", "negative", "blocked", "no_output", "exhausted", "stopped", "out-of-scope":
			default:
				continue
			}
			var p struct {
				Summary  string `json:"summary"`
				RiskArea string `json:"risk_area"`
			}
			_ = json.Unmarshal(n.Payload, &p)
			d := ExhaustionDirection{Summary: firstLine(p.Summary, 200), Outcome: n.State}
			// 该意图的产出：有 yields 边指向的事实/发现才算"有证据"。
			factIDs := intentFactIDs(ts, n.ID)
			if ev := evidenceDigestFromFacts(ts, factIDs); ev != "" {
				d.Evidence = ev
			}
			claim.Directions = append(claim.Directions, d)
			depthIntents[n.ID] = intentDepthEntry{area: p.RiskArea, facts: len(factIDs)}
			depthIDs = append(depthIDs, n.ID)
			kept++
		}
	}
	if as != nil && taskID > 0 {
		if roots, err := as.ScopeRootDomains(taskID); err == nil {
			if _, total, err := as.UntouchedSiblings(taskID, roots, 1); err == nil {
				claim.Coverage.UntouchedSilos = total
			}
			if n, err := as.CountSiblingsInGraph(roots); err == nil {
				claim.Coverage.InScopeAssets = n
			}
		}
		if _, shallow, err := ts.DepthGaps(taskID, db.DefaultDepthAreas, 1); err == nil {
			claim.Coverage.ShallowAssets = shallow
			claim.Coverage.DepthThreshold = db.DefaultDepthAreas
		}
	}
	claim.Coverage.Depth = buildDepthRows(ts, as, depthIDs, depthIntents)
	if rep, err := ts.ChainLedger(); err == nil {
		for _, r := range rep.Rows {
			if r.Status == db.ChainStatusUnused || r.Status == db.ChainStatusOpen {
				claim.Coverage.ChainsOpen++
			}
		}
	}
	if taskID > 0 {
		if cov, err := ts.CoverageMatrix(taskID); err == nil && len(cov) > 0 {
			waivers, _ := ts.CoverageWaivers()
			rep := db.BuildCoverageLedger(cov, waivers, db.RiskAreas)
			claim.Coverage.LedgerWaived = rep.Waived
			for _, c := range rep.CellsList {
				if !c.Waived && c.Status == db.LedgerFailed {
					claim.Coverage.LedgerFailed++
				}
			}
		}
	}
	return claim
}

// buildDepthRows 把「意图 → 锚定资产 × 风险类」折成复核员可读的投入面。
// 只作为判断依据，不参与硬门：任何单一数字都不足以证明深度，但"每条方向都是
// 1 条意图 1 条事实"足以说明深度不足。
func buildDepthRows(ts *db.ExplorationStore, as *db.AssetStore, ids []int64, entries map[int64]intentDepthEntry) []ExhaustionDepthRow {
	if ts == nil || len(ids) == 0 {
		return nil
	}
	nodeAssets, err := ts.NodeAssets(ids)
	if err != nil {
		return nil
	}
	var assetIDs []int64
	for _, list := range nodeAssets {
		assetIDs = append(assetIDs, list...)
	}
	labels := map[int64]string{}
	if as != nil {
		if assets, err := as.GetByIDs(assetIDs); err == nil {
			for _, a := range assets {
				labels[a.ID] = assetLabel(a)
			}
		}
	}
	type depthKey struct{ asset, area string }
	rows := map[depthKey]*ExhaustionDepthRow{}
	var order []depthKey
	for _, id := range ids {
		e := entries[id]
		if e.area == "" {
			e.area = "other"
		}
		for _, aid := range nodeAssets[id] {
			label := labels[aid]
			if label == "" {
				label = fmt.Sprintf("asset#%d", aid)
			}
			k := depthKey{label, e.area}
			row := rows[k]
			if row == nil {
				row = &ExhaustionDepthRow{Asset: label, RiskArea: e.area}
				rows[k] = row
				order = append(order, k)
			}
			row.Intents++
			row.Facts += e.facts
		}
	}
	out := make([]ExhaustionDepthRow, 0, len(order))
	for _, k := range order {
		out = append(out, *rows[k])
	}
	if len(out) > 60 {
		out = out[:60]
	}
	return out
}

// intentFactIDs 返回一条意图产出的事实 id（best-effort）。
func intentFactIDs(ts *db.ExplorationStore, intentID int64) []int64 {
	ids, err := ts.FactsYielded(intentID)
	if err != nil {
		return nil
	}
	return ids
}

// intentEvidenceDigest 取一条意图产出的证据摘要（yields 指向的事实）。
func intentEvidenceDigest(ts *db.ExplorationStore, intentID int64) string {
	return evidenceDigestFromFacts(ts, intentFactIDs(ts, intentID))
}

// evidenceDigestFromFacts 只给前两条、每条截断——复核员需要判断"有没有证据"，
// 不需要读全文。
func evidenceDigestFromFacts(ts *db.ExplorationStore, ids []int64) string {
	if len(ids) == 0 {
		return ""
	}
	if len(ids) > 2 {
		ids = ids[:2]
	}
	var parts []string
	for _, id := range ids {
		n, err := ts.GetNode(id)
		if err != nil || n == nil {
			continue
		}
		var p struct {
			Summary string `json:"summary"`
			Detail  string `json:"detail"`
		}
		_ = json.Unmarshal(n.Payload, &p)
		text := p.Summary
		if strings.TrimSpace(text) == "" {
			text = p.Detail
		}
		if text != "" {
			parts = append(parts, fmt.Sprintf("[fact#%d] %s", id, firstLine(text, 160)))
		}
	}
	return strings.Join(parts, "；")
}
