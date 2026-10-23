# 提示词 A/B 对照流程

内置提示词一改，**已部署实例不会自动更新**——`SeedPromptIfEmpty`（`db/config.go`）是
首次播种，只在该 agent 还没有任何提示词版本时写入。所以：

- 全新部署 / 新的 bench 容器 → 直接用新版；
- 已存在的实例 → 必须 `POST /api/agents/{key}/prompt/reset` 才会切到新版。

这也意味着 **A/B 的对照组不能靠"改回代码"**：一旦内置变了，旧文本就没了。
所以每次改内置提示词前，先把**改动前的完整模板**存成 `planner_baseline.md` 这类文件。

## 步骤

1. 改动前先存 baseline（当前 `planner_baseline.md` 就是"发散优先"强化之前的版本）：

   ```sh
   python3 - <<'PY'
   import re
   s = open("agent/planner.go").read()
   m = re.search(r'const plannerDefaultTmpl = `(.*?)`\n', s, re.S)
   open("bench/prompts/planner_baseline.md", "w").write(m.group(1))
   PY
   ```

2. **A 组（新版）**：新容器启动即播种新版；已存在容器先 reset。
3. **B 组（旧版）**：把 baseline 当作**完整模板** PUT 回去（它含 `{{.Goal}}` 等变量，
   校验会通过）：

   ```sh
   jq -Rs '{template:., note:"A/B baseline"}' bench/prompts/planner_baseline.md \
     | curl -s -X PUT "$API/api/agents/planner/prompt" -H "$AUTH" -H "$CT" --data-binary @-
   ```

4. 两组都设 `PROMPT_VARIANT`（如 `divergence` / `baseline`），它会写进
   `runs/<run_id>/summary.json` 的 `attribution.prompt_variant`。
5. 同挑战集、各 3 轮 `--runs 3`；判据见下。

## 判据

分数噪声大，**优先看发散度指标**：

| 指标 | 期望 |
|---|---|
| 每任务意图数（`n_intent`） | 上升 |
| `policy_blocked` / 提前收尾次数 | 下降 |
| 每解出一题的 token（`tokens_in + tokens_out` ÷ `solved`） | 不升超过 10% |
| 解出题数（3 轮中位数） | 不降 |

任一项越界即回滚：把 baseline PUT 回去即可，**不需要回滚代码**。

## 注意

- 每轮之间靶场与 BLADE 库需**外部重置**，否则上一轮已解出的题不会重新计分
  （`--runs` 的 help 里也写了）。
- 提示词是全局 DB 状态，**只能顺序跑**，不能并行 A/B。
