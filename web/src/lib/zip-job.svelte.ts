import { createSubscriber } from 'svelte/reactivity';
import {
	ApiError,
	errorMessage,
	getZip,
	isNotFound,
	startZip,
	type LinkKind,
	type ZipStatus
} from '#lib/api/index.ts';
import { download } from '#lib/download.ts';
import { sleep } from '#lib/retry.ts';

export type ZipState =
	| { kind: 'idle' }
	| { kind: 'starting' }
	/** The server is preparing too many zips; retrying. */
	| { kind: 'waiting' }
	| { kind: 'preparing'; done: number; total: number }
	| { kind: 'ready'; url: string; skipped: number }
	| { kind: 'failed'; message: string };

type Running = Extract<ZipState, { kind: 'starting' | 'waiting' | 'preparing' }> & {
	abort: AbortController;
};

type Settled = Exclude<ZipState, { kind: Running['kind'] }>;

type State = Settled | Running;

const POLL_MS = 1000;
const BUSY_RETRY_S = 5;
/** Restarts after the job vanished (e.g. a server restart) before giving up. */
const MAX_RESTARTS = 2;
const ZIP_UNAVAILABLE = 'Download unavailable';

/**
 * Prepares a zip of the share, or of the items in `ids`, then downloads it. Stops once nothing
 * reads `state`.
 */
export class ZipJob {
	readonly kind: LinkKind;
	readonly ref: string;
	readonly ids: readonly string[] | undefined;
	#state = $state.raw<State>({ kind: 'idle' });
	#subscribe: () => void;

	constructor(kind: LinkKind, ref: string, ids?: readonly string[]) {
		this.kind = kind;
		this.ref = ref;
		this.ids = ids;
		this.#subscribe = createSubscriber(() => () => this.cancel());
	}

	get state(): ZipState {
		this.#subscribe();
		return this.#state;
	}

	/** Starts or joins the job; downloads once ready (again when already ready). */
	start(): void {
		if (running(this.#state)) return;
		const abort = new AbortController();
		this.#state = { kind: 'starting', abort };
		void this.#run(abort).then((next) => {
			if (!this.#current(abort)) return;
			this.#state = next;
			if (next.kind === 'ready') download(next.url);
		});
	}

	cancel(): void {
		const state = this.#state;
		if (!running(state)) return;
		state.abort.abort();
		this.#state = { kind: 'idle' };
	}

	#current(abort: AbortController): boolean {
		const state = this.#state;
		return running(state) && state.abort === abort;
	}

	/** Shows progress of the run `abort` belongs to, unless it was cancelled. */
	#progress(abort: AbortController, next: Running): void {
		if (this.#current(abort)) this.#state = next;
	}

	async #run(abort: AbortController): Promise<Settled> {
		const { signal } = abort;
		try {
			for (let restarts = 0; ; restarts++) {
				const status = await this.#prepare(abort);
				if (status) return settle(status);
				if (restarts >= MAX_RESTARTS) return { kind: 'failed', message: ZIP_UNAVAILABLE };
			}
		} catch (err) {
			if (signal.aborted) return { kind: 'idle' };
			return { kind: 'failed', message: isNotFound(err) ? ZIP_UNAVAILABLE : errorMessage(err) };
		}
	}

	/** Starts or joins the job and polls it until it settles; null if the job vanished. */
	async #prepare(
		abort: AbortController
	): Promise<Exclude<ZipStatus, { state: 'preparing' }> | null> {
		const { signal } = abort;
		let status = await this.#startZip(abort);
		while (status.state === 'preparing') {
			signal.throwIfAborted();
			this.#progress(abort, { kind: 'preparing', done: status.done, total: status.total, abort });
			await sleep(POLL_MS, signal);
			try {
				status = await getZip(this.kind, this.ref, status.zipId, signal);
			} catch (err) {
				if (isNotFound(err)) return null;
				throw err;
			}
		}
		return status;
	}

	async #startZip(abort: AbortController): Promise<ZipStatus> {
		const { signal } = abort;
		for (;;) {
			try {
				return await startZip(this.kind, this.ref, this.ids);
			} catch (err) {
				if (signal.aborted || !(err instanceof ApiError) || err.code !== 'busy') throw err;
				this.#progress(abort, { kind: 'waiting', abort });
				await sleep((err.retryAfter ?? BUSY_RETRY_S) * 1000, signal);
			}
		}
	}
}

function settle(status: Exclude<ZipStatus, { state: 'preparing' }>): Settled {
	switch (status.state) {
		case 'ready':
			return { kind: 'ready', url: status.url, skipped: status.skipped };
		case 'failed':
			return { kind: 'failed', message: ZIP_UNAVAILABLE };
		default:
			return status satisfies never;
	}
}

function running(state: State): state is Running {
	return state.kind === 'starting' || state.kind === 'waiting' || state.kind === 'preparing';
}
