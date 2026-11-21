# 提示词 / 策略 A/B 对照流程

提示词正文在 DB 里有版本历史；0.9.23 起，旧的内置正文会被明确迁移到新出厂正文，
真正自定义的正文仍保留。升级代码**不保证**已部署实例立刻使用新正文，因此 A/B 仍要
显式固定两组的实际正文，而不是只看代码版本。

## 步骤

1. 改动前保存 baseline（完整模板；`planner_baseline.md` 是历史基线，不自动等于当前版本）：

   ```sh
   python3 - <<'PY'
   import re
   s = open("agent/planner.go").read()
   m = re.search(r'const plannerDefaultTmpl = `(.*?)`\n', s, re.S)
   open("bench/prompts/planner_baseline.md", "w").write(m.group(1))
   PY
   ```

2. **A 组**：新容器启动播种新正文；已有实例按需 `POST /api/agents/{key}/prompt/reset`
   或 PUT 完整模板。**B 组**：把 baseline 作为完整模板 PUT 回去。
3. 记住模板只是有效请求的一部分：append、渲染后的 system、工具 schema、任务级模型链
   都会改变实际输入。不要用“模板相同”推断两组运行条件相同。
4. 两组都设置 `PROMPT_VARIANT`。`summary.json` 会记录 git、实际模板+append 指纹、
   非密钥模型 profile，以及从真实调用采样得到的脱敏 `request-surfaces.json`
   （system/tools/参数/路径 SHA、实际模型、调用数、缓存命中 token；不含正文、密钥或目标内容）。
5. 同挑战集、各 3 轮 `--runs 3`；两组运行之间必须外部重置靶场与 BLADE 库。

## 判据

分数噪声大，**先看能否归因，再看效果**。有效计划和更少无效动作优先于“意图更多”。

| 指标 | 期望 |
|---|---|
| 有效独立发现 / 有证据的据点、凭据 | 上升或不降 |
| 线索接手比例与首次接手延迟 | 上升、缩短 |
| 各方向首次执行等待（长期未执行方向） | 缩短、减少 |
| 重复探索、无新增信息 `done`、无产出比例 | 下降 |
| 每解出一题的 token（含缓存命中成本） | 不升超过 10% |
| 解出题数（3 轮中位数） | 不降 |

`n_intent` 只作为过程描述：更多意图可能是进步，也可能只是刷管理动作，**不能单独作为
成功判据**。`request-surfaces` 只证明“实际请求结构是什么”，不证明缓存命中或模型更强。

## 注意

- 每轮之间靶场与 BLADE 库需外部重置，否则上一轮已解出的题不会重新计分。
- 提示词是全局 DB 状态，**只能顺序跑**，不能并行 A/B。
- 任一项越界即回滚到 baseline；不要以一次运行或模拟 run 判断能力提升。
