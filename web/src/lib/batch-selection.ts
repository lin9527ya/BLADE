// Selection belongs to IDs, not to the visible page. Keep failures and anything
// selected while the request was in flight; remove only acknowledged successes.
export function remainingSelection(
	selected: Set<string>,
	results: { id: string; ok: boolean }[],
): Set<string> {
	const next = new Set(selected);
	for (const item of results) if (item.ok) next.delete(item.id);
	return next;
}
