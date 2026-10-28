// Per-session MAX_KEEP is not a global bound: several opened sessions can each
// hold thousands of activities. Evict the oldest cold, fully-loaded sessions
// down to an accounting tail; selecting them again triggers the normal latest-page
// load. The active session and any in-flight load are never touched.
export const MAX_TOTAL_KEEP = 16_000;
export const COLD_TAIL_KEEP = 4;

export interface CacheActivity {
	seq: number;
	kind: string;
	ts: string;
}

export interface CacheSession {
	items: CacheActivity[];
	loaded: boolean;
	loading: boolean;
	loadingMore: boolean;
	hasMore: boolean;
	earliestSeq: number;
	firstTs: string;
	lastTs: string;
	unread: number;
	error?: string;
}

function accountingTail(items: CacheActivity[]): CacheActivity[] {
	return items
		.filter((item) => item.kind === "usage" || item.kind === "result")
		.slice(-COLD_TAIL_KEEP);
}

export function capSessionStore<T extends CacheSession>(
	store: Record<string, T>,
	activeKey: string,
	maxTotal = MAX_TOTAL_KEEP,
): Record<string, T> {
	let total = 0;
	for (const session of Object.values(store)) total += session.items.length;
	if (total <= maxTotal) return store;

	const candidates = Object.entries(store)
		.filter(([key, session]) => {
			return (
				key !== activeKey &&
				session.loaded &&
				!session.loading &&
				!session.loadingMore &&
				session.items.length > COLD_TAIL_KEEP
			);
		})
		.sort(([, a], [, b]) => (a.lastTs || "").localeCompare(b.lastTs || ""));

	let next = store;
	for (const [key, session] of candidates) {
		if (total <= maxTotal) break;
		const tail = accountingTail(session.items);
		const reduced = {
			...session,
			items: tail,
			loaded: false,
			loading: false,
			loadingMore: false,
			hasMore: true,
			earliestSeq: 0,
			error: undefined,
		} as T;
		total -= session.items.length - tail.length;
		next = { ...next, [key]: reduced };
	}
	return next;
}
