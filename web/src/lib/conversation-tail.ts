// Run state and transport state are separate: an ended turn can still have
// several pages behind its cursor. Only stop after the last page is consumed.
export function pollConversationTail<
	T extends { cursor: number; running: boolean; hasMore: boolean },
>(options: {
	cursor: () => number;
	fetchPage: (cursor: number, signal: AbortSignal) => Promise<T>;
	receive: (page: T) => void;
	drained: () => void;
	failed?: (error: unknown) => void;
	intervalMs?: number;
	maxConsecutiveErrors?: number;
}) {
	const controller = new AbortController();
	let timer: ReturnType<typeof setTimeout> | undefined;
	let stopped = false;
	let errors = 0;
	const stop = (callback?: (error?: unknown) => void, error?: unknown) => {
		if (stopped || controller.signal.aborted) return;
		stopped = true;
		callback?.(error);
	};
	const tick = async () => {
		let delay = options.intervalMs ?? 1000;
		let again = true;
		try {
			const before = options.cursor();
			const page = await options.fetchPage(before, controller.signal);
			if (controller.signal.aborted) return;
			errors = 0;
			options.receive(page);
			if (page.hasMore && page.cursor <= before) {
				stop(options.failed, new Error("尾拉游标没有推进"));
				return;
			}
			again = page.running || page.hasMore;
			if (page.hasMore) delay = 0; // Drain backlog without waiting one second per page.
			if (!again) {
				stop(options.drained);
				return;
			}
		} catch (error) {
			if (controller.signal.aborted) return;
			errors++;
			if (errors >= (options.maxConsecutiveErrors ?? 8)) {
				stop(options.failed, error);
				return;
			}
			// A transient transport error must not be mistaken for an empty tail.
			delay = Math.min((options.intervalMs ?? 1000) * 2 ** errors, 15_000);
		}
		if (!stopped && !controller.signal.aborted && again)
			timer = setTimeout(() => void tick(), delay);
	};
	void tick();
	return () => {
		controller.abort();
		clearTimeout(timer);
	};
}
