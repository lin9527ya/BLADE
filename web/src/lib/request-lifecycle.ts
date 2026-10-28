// One owner for manual refreshes and polls. Superseded work may finish, but may
// not publish data, errors or loading state into its replacement.
export function createLatestRequest() {
	let controller: AbortController | null = null;
	const cancel = () => {
		controller?.abort();
		controller = null;
	};
	return {
		cancel,
		start() {
			cancel();
			const current = new AbortController();
			controller = current;
			return {
				signal: current.signal,
				alive: () => controller === current && !current.signal.aborted,
			};
		},
	};
}

export async function runLatestRequest<T>(
	owner: ReturnType<typeof createLatestRequest>,
	fetchValue: (signal: AbortSignal) => Promise<T>,
	callbacks: {
		success: (value: T) => void;
		error: (error: unknown) => void;
		settled: () => void;
	},
): Promise<void> {
	const request = owner.start();
	try {
		const value = await fetchValue(request.signal);
		if (request.alive()) callbacks.success(value);
	} catch (error) {
		if (request.alive()) {
			callbacks.error(error);
			throw error; // The polling owner must see failure to apply backoff.
		}
	} finally {
		if (request.alive()) callbacks.settled();
	}
}

export function abortableDelay(ms: number, signal: AbortSignal): Promise<void> {
	return new Promise((resolve, reject) => {
		if (signal.aborted) {
			reject(signal.reason);
			return;
		}
		const abort = () => {
			clearTimeout(timer);
			signal.removeEventListener("abort", abort);
			reject(signal.reason);
		};
		const timer = setTimeout(() => {
			signal.removeEventListener("abort", abort);
			resolve();
		}, ms);
		signal.addEventListener("abort", abort, { once: true });
	});
}

export class HttpError extends Error {
	readonly status: number;
	constructor(message: string, status: number) {
		super(message);
		this.name = "HttpError";
		this.status = status;
	}
}

export async function retryRequest<T>(
	fetchValue: (signal: AbortSignal) => Promise<T>,
	signal: AbortSignal,
	options: {
		attempts?: number;
		delayMs?: number;
		wait?: typeof abortableDelay;
	} = {},
): Promise<T> {
	const attempts = options.attempts ?? 3;
	const wait = options.wait ?? abortableDelay;
	for (let attempt = 0; ; attempt++) {
		signal.throwIfAborted();
		try {
			const value = await fetchValue(signal);
			signal.throwIfAborted();
			return value;
		} catch (error) {
			signal.throwIfAborted();
			if (
				attempt + 1 >= attempts ||
				(error instanceof HttpError &&
					error.status < 500 &&
					error.status !== 429)
			) {
				throw error;
			}
			await wait((options.delayMs ?? 2000) * 2 ** attempt, signal);
		}
	}
}
