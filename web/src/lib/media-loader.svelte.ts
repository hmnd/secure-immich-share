import { createSubscriber } from 'svelte/reactivity';
import {
	errorMessage,
	isNotFound,
	prepareMedia,
	type LinkKind,
	type MediaVariant
} from '#lib/api/index.ts';
import { sleep } from '#lib/retry.ts';

type MediaPhase = 'queued' | 'processing' | 'busy';

export type MediaState =
	| { kind: 'idle' }
	| { kind: 'checking' }
	| { kind: 'preparing'; phase: MediaPhase }
	| { kind: 'ready'; url: string }
	| { kind: 'failed'; message: string };

export type MediaResult = Extract<MediaState, { kind: 'ready' | 'failed' }>;

interface MediaTarget {
	kind: LinkKind;
	ref: string;
	id: string;
	variant: MediaVariant;
}

const MIN_POLL_MS = 250;
const MAX_POLL_MS = 10_000;
export const NOT_AVAILABLE = 'Not available';

/**
 * Polls `prepareMedia` until the variant is ready or failed; rejects once `signal` aborts.
 * The server job keeps running either way.
 */
async function pollMedia(
	target: MediaTarget,
	signal: AbortSignal,
	onPhase: (phase: MediaPhase) => void = () => {}
): Promise<MediaResult> {
	const { kind, ref, id, variant } = target;
	try {
		for (;;) {
			const status = await prepareMedia(kind, ref, id, variant, signal);
			signal.throwIfAborted();
			switch (status.state) {
				case 'ready':
					return { kind: 'ready', url: status.url };
				case 'failed':
					return { kind: 'failed', message: NOT_AVAILABLE };
				case 'queued':
				case 'processing':
				case 'busy':
					onPhase(status.state);
					await sleep(Math.min(MAX_POLL_MS, Math.max(MIN_POLL_MS, status.retryAfterMs)), signal);
					break;
				default:
					status satisfies never;
			}
		}
	} catch (err) {
		signal.throwIfAborted();
		return { kind: 'failed', message: isNotFound(err) ? NOT_AVAILABLE : errorMessage(err) };
	}
}

/** Prepares one variant; rejects once `signal` aborts. */
export type Prepare = (signal: AbortSignal) => Promise<MediaResult>;

export const preparer =
	(target: MediaTarget): Prepare =>
	(signal) =>
		pollMedia(target, signal);

export type Fetched<T> =
	| { kind: 'taken'; value: T; url: string }
	/** Every variant that was ready got refused; `url` is the first of them. */
	| { kind: 'refused'; url: string }
	| { kind: 'unavailable' };

/**
 * Prepares and fetches each variant in turn until `take` accepts the response (null refuses
 * it). Variants the server can't provide are skipped; other failures throw so they can be
 * retried.
 */
export async function fetchPrepared<T>(
	variants: readonly Prepare[],
	signal: AbortSignal,
	take: (res: Response) => Promise<T | null>
): Promise<Fetched<T>> {
	let refused: string | null = null;
	for (const prepare of variants) {
		const result = await prepare(signal);
		if (result.kind === 'failed') {
			if (result.message === NOT_AVAILABLE) continue;
			throw new Error(result.message);
		}
		const res = await fetch(result.url, { signal });
		if (res.status === 404) continue;
		if (!res.ok) throw new Error(`HTTP ${res.status}`);
		const value = await take(res);
		if (value !== null) return { kind: 'taken', value, url: result.url };
		if (!res.bodyUsed) await res.body?.cancel();
		refused ??= result.url;
	}
	return refused === null ? { kind: 'unavailable' } : { kind: 'refused', url: refused };
}

interface Run {
	abort: AbortController;
	/** Resolves with the final state, or `idle` once cancelled. */
	done: Promise<MediaState>;
}

type State =
	| Exclude<MediaState, { kind: 'checking' | 'preparing' }>
	| { kind: 'checking'; run: Run }
	| { kind: 'preparing'; phase: MediaPhase; run: Run };

/**
 * Prepares one variant of one item for display, polling until it is ready or failed.
 * Polling stops on `cancel()` or once nothing reads `state`; the server job keeps running.
 * With `auto`, reading `state` starts it.
 */
export class MediaLoader {
	readonly target: MediaTarget;
	#state = $state.raw<State>({ kind: 'idle' });
	#subscribe: () => void;

	constructor(target: MediaTarget, auto = false) {
		this.target = target;
		this.#subscribe = createSubscriber(() => {
			if (auto) void this.start();
			return () => this.cancel();
		});
	}

	get state(): MediaState {
		this.#subscribe();
		return this.#state;
	}

	/** Starts (or retries after a failure); resolves with the final state, or `idle` if cancelled. */
	start(): Promise<MediaState> {
		const state = this.#state;
		switch (state.kind) {
			case 'ready':
				return Promise.resolve(state);
			case 'checking':
			case 'preparing':
				return state.run.done;
			case 'idle':
			case 'failed':
				break;
			default:
				state satisfies never;
		}
		const abort = new AbortController();
		const run: Run = {
			abort,
			done: pollMedia(this.target, abort.signal, (phase) => {
				if (this.#current(run)) this.#state = { kind: 'preparing', phase, run };
			}).then(
				(next) => {
					if (this.#current(run)) this.#state = next;
					return next;
				},
				(): MediaState => ({ kind: 'idle' })
			)
		};
		this.#state = { kind: 'checking', run };
		return run.done;
	}

	/** Like `start`, but cancels once `signal` aborts and rejects then. */
	async prepare(signal: AbortSignal): Promise<MediaResult> {
		const cancel = () => this.cancel();
		signal.addEventListener('abort', cancel, { once: true });
		try {
			const state = await this.start();
			signal.throwIfAborted();
			if (state.kind !== 'ready' && state.kind !== 'failed') {
				throw new DOMException('Cancelled', 'AbortError');
			}
			return state;
		} finally {
			signal.removeEventListener('abort', cancel);
		}
	}

	/** Marks a ready variant unusable, e.g. once the browser can't play it. */
	fail(): void {
		if (this.#state.kind === 'ready') this.#state = { kind: 'failed', message: NOT_AVAILABLE };
	}

	cancel(): void {
		const state = this.#state;
		if (state.kind !== 'checking' && state.kind !== 'preparing') return;
		state.run.abort.abort();
		this.#state = { kind: 'idle' };
	}

	#current(run: Run): boolean {
		const state = this.#state;
		return (state.kind === 'checking' || state.kind === 'preparing') && state.run === run;
	}
}
