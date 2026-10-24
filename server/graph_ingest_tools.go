package server

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	actool "github.com/lin9527ya/Norma/tool"

	"github.com/lin9527ya/blade/agent"
	"github.com/lin9527ya/blade/db"
)

// 图数据摄入工具（P40）：把 agent **在目标里跑出来的**枚举结果直接落进图。
//
// 为什么需要它：域内图（P32）与云上图（P39）此前只有**页面导入**一个入口——而现实里最自然的
// 获取时刻恰恰是打进去之后：在域内机器上跑 net group /domain 或 SharpHound，拿着偷到的 key 跑
// `aws iam get-account-authorization-details`。那时产物只能压进 fact 与执行台账，图还是空的，
// 规划层也就看不到"从这点能走到哪"——而图上多一条边少一条边，直接决定下一轮派什么意图
// （这正是 P27/P29 战果层当初要解决的问题，这里只是同一件事的延伸）。
//
// 两个工具、与页面导入**同一份实现**（planADImport/applyADImport、planCloudImport/applyCloudImport）：
// 页面能导什么、agent 就能录什么，反之亦然。谁也不会漂移成两套口径。
//
// 它不做范围判定：摄入是**读已经拿到的数据**、不产生对目标的动作（与页面导入同理），
// 所以不过 RoE/命令门；拿它去"据此打"仍走既有的意图与命令门。
func (s *Server) graphIngestTools() []actool.CoreTool {
	return []actool.CoreTool{s.toolIngestADData(), s.toolIngestCloudData()}
}

// ingestInput 是两个工具共用的入参。
type ingestInput struct {
	Paths []string `json:"paths"`
	JSON  string   `json:"json"`
}

// ingestFiles 把入参变成待解析的文件清单：目录展开一层（SharpHound 解包后的目录直接用），
// 单文件直接读；`json` 作为一份内联文件参与（小份数据、或 agent 自己拼的片段）。
//
// 读不进来的路径如实报错，不静默跳过——"我明明导了"却什么都没进图，是最难查的一类。
func ingestFiles(in ingestInput) ([]graphImportFile, error) {
	var out []graphImportFile
	for _, p := range in.Paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		st, err := os.Stat(p)
		if err != nil {
			return nil, fmt.Errorf("路径读不到：%s（%v）", p, err)
		}
		if st.IsDir() {
			entries, err := os.ReadDir(p)
			if err != nil {
				return nil, fmt.Errorf("目录读不到：%s（%v）", p, err)
			}
			names := make([]string, 0, len(entries))
			for _, e := range entries {
				if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".json") {
					continue
				}
				names = append(names, e.Name())
			}
			sort.Strings(names)
			for _, name := range names {
				full := filepath.Join(p, name)
				fi, err := os.Stat(full)
				if err != nil {
					return nil, fmt.Errorf("文件读不到：%s（%v）", full, err)
				}
				if fi.Size() > maxIngestFileBytes {
					return nil, fmt.Errorf("文件过大（%d 字节，上限 %d）：%s——枚举结果请先精简或分批导", fi.Size(), maxIngestFileBytes, full)
				}
				raw, err := os.ReadFile(full)
				if err != nil {
					return nil, fmt.Errorf("读取失败：%s（%v）", full, err)
				}
				out = append(out, graphImportFile{Name: name, JSON: string(raw)})
			}
			continue
		}
		if st.Size() > maxIngestFileBytes {
			return nil, fmt.Errorf("文件过大（%d 字节，上限 %d）：%s", st.Size(), maxIngestFileBytes, p)
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("读取失败：%s（%v）", p, err)
		}
		out = append(out, graphImportFile{Name: filepath.Base(p), JSON: string(raw)})
	}
	if s := strings.TrimSpace(in.JSON); s != "" {
		if len(s) > maxIngestFileBytes {
			return nil, fmt.Errorf("内联 JSON 过大（上限 %d 字节）：请落盘后传 paths", maxIngestFileBytes)
		}
		out = append(out, graphImportFile{Name: "（内联 JSON）", JSON: s})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("paths 与 json 至少要给一个：要解析哪份枚举结果？")
	}
	return out, nil
}

// maxIngestFileBytes 单份文件上限。SharpHound 的全量 users.json 可能几十 MB，
// 32MB 是"够用且不至于把一次工具调用的上下文与内存拖垮"的折中；超了如实报错让调用方分批。
const maxIngestFileBytes = 32 << 20

func (s *Server) toolIngestADData() actool.CoreTool {
	return wrTool("ingest_ad_data",
		"把**域内枚举结果**落进本任务的图：吃 SharpHound / BloodHound 导出的 JSON（users.json / groups.json / computers.json / domains.json…）。"+
			"用在：你在域内机器上跑出了采集结果（或拿到别人跑的结果）——**记进图里，规划层才看得到「从这点能走到哪」**，"+
			"否则它只是一段文本。落进去之后用 ad_paths 读攻击路径。\n"+
			"输入：paths（文件绝对路径，或一个目录——目录里所有 .json 都会被读）与/或 json（内联文本）。"+
			"重导只合并（节点身份是 SID），所以同一份结果导两次不会重复。\n"+
			"它不产生对目标的动作（读的是已经拿到的数据），因此不过 RoE/命令门；拿结果去打仍走既有的门。",
		objSchema(map[string]any{
			"paths": map[string]any{
				"type": "array", "items": map[string]any{"type": "string"},
				"description": "采集结果文件（或目录）的绝对路径，如 /data/tasks/3/i20/task-output/bloodhound/",
			},
			"json": strParam("或者直接给 JSON 文本（小份数据用；大文件请走 paths）"),
		}),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a ingestInput
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			files, err := ingestFiles(a)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			store, _, err := s.ingestStoreFor(ctx)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			plan, err := planADImport(store, files)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			out := s.applyADImport(plan)
			return actool.Text(renderIngestReceipt("域内", files, plan.Plan.objectCounts(), plan.Plan.edgeCounts(),
				plan.Plan.highValue(8), plan.Plan.Warnings, out.Created, out.Updated, out.Linked, out.Relinked, out.Failed, out.Errs)), nil
		})
}

func (s *Server) toolIngestCloudData() actool.CoreTool {
	return wrTool("ingest_cloud_data",
		"把**云上枚举结果**落进本任务的图：吃两种离线输出——"+
			"① AWS `aws iam get-account-authorization-details` 的 JSON（授权明细：用户/组/角色/策略/信任）；"+
			"② K8s `kubectl get clusterroles,clusterrolebindings,roles,rolebindings,serviceaccounts -A -o json` 的输出。\n"+
			"用在：你拿到了云凭据或已经在云里（例如从被控主机上读到 access key）——把授权枚举结果记进图，"+
			"规划层才看得到「谁能拿到 admin 策略 / 集群管理员」。落进去之后用 ad_paths 读攻击路径"+
			"（域内与云上共用同一条算法，两者还能连成一条链）。\n"+
			"**平台不自己去调云 API**：枚举在有凭据的那台机器上用 aws-cli / kubectl 做，这里只读结果。"+
			"输入 paths（文件或目录）与/或 json；重导只合并（身份键是 ARN / k8s 的 kind:ns:name）。",
		objSchema(map[string]any{
			"paths": map[string]any{
				"type": "array", "items": map[string]any{"type": "string"},
				"description": "枚举结果文件（或目录）的绝对路径",
			},
			"json": strParam("或者直接给 JSON 文本（小份数据用）"),
		}),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a ingestInput
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			files, err := ingestFiles(a)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			store, _, err := s.ingestStoreFor(ctx)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			plan, err := planCloudImport(store, files)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			out := s.applyCloudImport(plan)
			receipt := renderIngestReceipt("云上", files, plan.Plan.objectCounts(), plan.Plan.edgeCounts(),
				plan.Plan.highValue(8), plan.Plan.Warnings, out.Created, out.Updated, out.Linked, out.Relinked, out.Failed, out.Errs)
			if len(plan.Plan.Skipped) > 0 {
				receipt += "\n未建模（如实计数）：" + renderCounts(plan.Plan.Skipped)
			}
			return actool.Text(receipt), nil
		})
}

// ingestStoreFor 取本次运行的探索图。没有任务上下文就不给导——图数据总得挂在某张图上，
// 凭空造一个"全局图"是另一个模型，不在这一刀里。
func (s *Server) ingestStoreFor(ctx context.Context) (*db.ExplorationStore, string, error) {
	ri := agent.RunInfoFrom(ctx)
	if ri.ExplorationID <= 0 {
		return nil, "", fmt.Errorf("这个工具要在任务上下文里用：摄入的数据要落进某个任务的图（对话页没有任务图）")
	}
	return s.m.pg.Exploration(ri.ExplorationID), "", nil
}

// renderIngestReceipt 是一份"导进去了什么"的回执。
//
// 为什么回执要这么细：摄入是**写图**的动作，而图直接决定下一轮派什么意图。少了哪一块、
// 哪条边没解析出来（对象没采集到、权限名不在白名单…），调用方要能当场看见——不然它会
// 照着一个残缺的图去规划。
func renderIngestReceipt(label string, files []graphImportFile, objects, edges map[string]int,
	highvalue []map[string]any, warnings []string,
	created, updated, linked, relinked, failed int, errs []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s枚举结果已摄入：新增 %d 个对象、合并 %d 个；新建 %d 条关系、合并 %d 条。\n",
		label, created, updated, linked, relinked)
	names := make([]string, 0, len(files))
	for _, f := range files {
		names = append(names, f.displayName())
	}
	fmt.Fprintf(&b, "文件：%s\n", strings.Join(names, "、"))
	if len(objects) > 0 {
		fmt.Fprintf(&b, "对象：%s\n", renderCounts(objects))
	}
	if len(edges) > 0 {
		fmt.Fprintf(&b, "关系：%s\n", renderCounts(edges))
	}
	if len(highvalue) > 0 {
		names := make([]string, 0, len(highvalue))
		for _, h := range highvalue {
			names = append(names, fmt.Sprint(h["name"]))
		}
		fmt.Fprintf(&b, "高价值目标（前 %d 个）：%s\n", len(highvalue), strings.Join(names, "、"))
	}
	if len(warnings) > 0 {
		fmt.Fprintf(&b, "提醒：%s\n", strings.Join(warnings, "；"))
	}
	if failed > 0 {
		fmt.Fprintf(&b, "失败 %d 项：%s\n", failed, strings.Join(errs, "；"))
	}
	b.WriteString("下一步：用 ad_paths 读攻击路径（域内与云上共用同一条算法）。")
	return b.String()
}

func renderCounts(counts map[string]int) string {
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s %d", k, counts[k]))
	}
	return strings.Join(parts, " · ")
}
