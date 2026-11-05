#!/usr/bin/env bash
# 从 CHANGELOG.md 里抽出某个版本的正文（发布时作为 GitHub Release 的正文）。
#
# 用法：./scripts/changelog-section.sh v0.7.7 [CHANGELOG.md]
#
# 为什么需要：release.yml 此前用 `generate_release_notes: true`，正文是 GitHub 按提交
# 自动生成的列表——与仓库里维护的 CHANGELOG 是两套东西，用户拿到的版本说明从来没人
# 读过。这里把"发布说明 = CHANGELOG 的对应段落"变成事实。
#
# 找不到该版本（例如只打了 tag 没写 changelog）时**返回非零**：宁可让发布 job 失败、
# 逼着补一段，也不要悄悄退回自动生成的提交列表。
set -euo pipefail

tag="${1:-}"
file="${2:-CHANGELOG.md}"
[ -n "$tag" ] || { echo "用法：$0 <tag> [CHANGELOG.md]" >&2; exit 2; }
[ -f "$file" ] || { echo "找不到 $file" >&2; exit 2; }

# v0.7.7 → 0.7.7；也接受已经是 0.7.7 的写法。
ver="${tag#v}"

rc=0
awk -v ver="$ver" '
  # 段落标题形如：## [0.7.7] - 2026-10-31  或  ## [0.7.7]
  function is_heading(line,   m) {
    return line ~ ("^## \\[" ver "\\]([^0-9]|$)")
  }
  found && /^## \[/ { exit }          # 下一段标题 = 本文结束
  found { print }
  !found && is_heading($0) { found = 1 }
  END { if (!found) exit 3 }
' "$file" || rc=$?

# 缺段落时把原因写清楚：workflow 里只看到 exit 3 的话，没人知道该补什么。
# 正文照旧不输出（调用方通常重定向到文件），失败路径不产生半份说明。
if [ "$rc" -eq 3 ]; then
  printf '%s\n' \
    "找不到「## [$ver]」段（tag: $tag，文件: $file）。" \
    "发布正文取自 CHANGELOG，缺段落时不会退回自动生成的提交列表——这是有意的。" \
    "补法：在 $file 的 [Unreleased] 之下加一段" \
    "  ## [$ver] - $(date +%F)" \
    "  <这一版改了什么、要注意什么>" \
    "然后重新打（或移动）tag 再发布。" >&2
fi
exit "$rc"
