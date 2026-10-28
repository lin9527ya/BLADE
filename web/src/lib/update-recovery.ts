// The probe is authenticated system/info after a ready health response, never
// the anonymous health payload itself. Bound both polling and an in-flight probe.
export async function waitForVersion(options: {
	from: string;
	target?: string;
	signal: AbortSignal;
	probe: (signal: AbortSignal) => Promise<string | null>;
	wait: (ms: number, signal: AbortSignal) => Promise<void>;
	terminalError: (error: unknown) => boolean;
	timeoutMs: number;
	intervalMs?: number;
}): Promise<string> {
	const deadline = new AbortController();
	const signal = AbortSignal.any([options.signal, deadline.signal]);
	const timer = setTimeout(
		() => deadline.abort(new Error("等待服务重启超时，请检查后端日志")),
		options.timeoutMs,
	);
	const normalize = (version: string) => version.replace(/^v/, "");
	try {
		while (true) {
			await options.wait(options.intervalMs ?? 2000, signal);
			signal.throwIfAborted();
			try {
				const version = await options.probe(signal);
				signal.throwIfAborted();
				if (!version || normalize(version) === normalize(options.from))
					continue;
				if (
					options.target &&
					normalize(version) !== normalize(options.target)
				) {
					throw new Error(
						`服务恢复到 ${version}，未安装预期版本 ${options.target}（可能已自动回滚）`,
					);
				}
				return version;
			} catch (error) {
				signal.throwIfAborted();
				if (
					options.terminalError(error) ||
					(error instanceof Error && error.message.includes("未安装预期版本"))
				) {
					throw error;
				}
				// The old process disappears during restart; transport/503 errors retry.
			}
		}
	} finally {
		clearTimeout(timer);
		deadline.abort();
	}
}
