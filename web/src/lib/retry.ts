/** Resolves after `ms`, or rejects with the abort reason once `signal` aborts. */
export function sleep(ms: number, signal: AbortSignal): Promise<void> {
	return new Promise((resolve, reject) => {
		if (signal.aborted) {
			reject(signal.reason);
			return;
		}
		const onAbort = () => {
			clearTimeout(timer);
			reject(signal.reason);
		};
		const timer = setTimeout(() => {
			signal.removeEventListener('abort', onAbort);
			resolve();
		}, ms);
		signal.addEventListener('abort', onAbort, { once: true });
	});
}

const MAX_ATTEMPTS = 5;

const isActive = () => document.visibilityState === 'visible' && navigator.onLine;

/**
 * Resolves once the page is visible and online. Android suspends the network of a
 * backgrounded browser, so every request fails instantly until it comes back.
 */
function whenActive(signal: AbortSignal): Promise<void> {
	if (isActive()) return Promise.resolve();
	return new Promise((resolve, reject) => {
		const listeners = new AbortController();
		const check = () => {
			if (!isActive()) return;
			listeners.abort();
			resolve();
		};
		const opts = { signal: listeners.signal };
		document.addEventListener('visibilitychange', check, opts);
		window.addEventListener('online', check, opts);
		signal.addEventListener(
			'abort',
			() => {
				listeners.abort();
				reject(signal.reason);
			},
			opts
		);
	});
}

/**
 * Runs `task` while the page is active, retrying with backoff when it throws, unless the
 * error is `fatal` or attempts run out. Return values (not throws) mark permanent outcomes.
 */
export async function withRetries<T>(
	task: () => Promise<T>,
	signal: AbortSignal,
	fatal: (err: unknown) => boolean = () => false
): Promise<T> {
	for (let attempt = 1; ; attempt++) {
		await whenActive(signal);
		try {
			return await task();
		} catch (err) {
			if (signal.aborted || fatal(err) || attempt >= MAX_ATTEMPTS) throw err;
			await sleep(1000 * 2 ** (attempt - 1), signal);
		}
	}
}
