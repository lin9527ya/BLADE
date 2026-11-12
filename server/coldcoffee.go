package server

import "strings"

// coldCoffeeSkillName is the directory name of the built-in ColdCoffee skill
// shipped in skills/coldcoffee. Keep it equal to the SKILL.md frontmatter name:
// visibility matches directories, while the Skill tool matches the callable name.
const coldCoffeeSkillName = "coldcoffee"

// coldCoffeeCueText is a code-owned activation cue, deliberately not stored in
// agent_prompts: user prompt edits and "reset to default" must not remove it,
// and it disappears automatically when the skill is toggled off for an agent.
const coldCoffeeCueText = "冷咖啡激活：当用户整条消息只有「冷咖啡」或「cold coffee」时，先调用 Skill 工具加载 coldcoffee，再按它的激活契约逐字输出激活页；其它任何消息都不要输出激活页。"

// coldCoffeeCue returns the cue for agents that can see the built-in skill.
// Only the human-facing agents (auto / pentest / mainagent) receive it; the
// orchestration agents (planner / worker / goals) work from intents rather than
// the user's literal activation word.
func coldCoffeeCue(agentKey string, skillNames []string) string {
	switch agentKey {
	case "auto", "pentest", "mainagent":
	default:
		return ""
	}
	for _, name := range skillNames {
		if name == coldCoffeeSkillName {
			return coldCoffeeCueText
		}
	}
	return ""
}

// appendColdCoffeeCue merges the code-owned activation cue after an existing
// DB-stored append segment. The DB segment, when present, stays first so a
// benchmark or operator instruction is not silently reordered; the cue follows
// it and is the last thing before the code-owned tails.
func appendColdCoffeeCue(agentKey, appendText string, skillNames []string) string {
	cue := coldCoffeeCue(agentKey, skillNames)
	if cue == "" {
		return appendText
	}
	if strings.TrimSpace(appendText) == "" {
		return cue
	}
	return strings.TrimSpace(appendText) + "\n\n" + cue
}
