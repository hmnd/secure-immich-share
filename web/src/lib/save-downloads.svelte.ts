import { createSubscriber } from 'svelte/reactivity';
import type { LinkKind, ShareItem } from '#lib/api/index.ts';
import { download } from '#lib/download.ts';
import { NOT_AVAILABLE, preparer } from '#lib/media-loader.svelte.ts';
import { sleep, withRetries } from '#lib/retry.ts';
import { CONCURRENCY, inOrder, keepAwake, saveVariants } from '#lib/saving.ts';

export type SaveDownloadsState =
	| { kind: 'idle' }
	| { kind: 'saving'; done: number; total: number }
	| { kind: 'done'; skipped: number }
	| { kind: 'failed'; message: string };

type State =
	| Exclude<SaveDownloadsState, { kind: 'saving' }>
	| { kind: 'saving'; abort: AbortController; done: number; total: number };

const DOWNLOADS_FAILED = 'Couldn’t download these items';
/** Browsers can drop downloads that start back to back. */
const DOWNLOAD_GAP_MS = 500;

/**
 * Saves items as separate downloads, one after another in share order. On Android they land
 * in the Downloads folder, where the gallery finds them. Stops once nothing reads `state`.
 */
export class SaveDownloadsJob {
	readonly method = 'downloads';
	readonly kind: LinkKind;
	readonly ref: string;
	readonly items: readonly ShareItem[];
	#state = $state.raw<State>({ kind: 'idle' });
	#subscribe: () => void;

	constructor(kind: LinkKind, ref: string, items: readonly ShareItem[]) {
		this.kind = kind;
		this.ref = ref;
		this.items = items;
		this.#subscribe = createSubscriber(() => () => this.cancel());
	}

	get state(): SaveDownloadsState {
		this.#subscribe();
		return this.#state;
	}

	get busy(): boolean {
		return this.state.kind === 'saving';
	}

	/** Downloads the items at `indices` (every item without them). */
	save(indices?: readonly number[]): void {
		if (this.#state.kind === 'saving') return;
		const positions = indices ?? this.items.map((_, index) => index);
		const abort = new AbortController();
		this.#state = { kind: 'saving', abort, done: 0, total: positions.length };
		void this.#run(positions, abort).then(
			(next) => this.#update(abort, next),
			() => this.#update(abort, { kind: 'failed', message: DOWNLOADS_FAILED })
		);
	}

	cancel(): void {
		const state = this.#state;
		if (state.kind !== 'saving') return;
		state.abort.abort();
		this.#state = { kind: 'idle' };
	}

	/** Hides a finished save's outcome. */
	dismiss(): void {
		const { kind } = this.#state;
		if (kind === 'done' || kind === 'failed') this.#state = { kind: 'idle' };
	}

	/** Moves the run `abort` belongs to on to `next`, unless it was cancelled or replaced. */
	#update(abort: AbortController, next: State): void {
		const state = this.#state;
		if (state.kind === 'saving' && state.abort === abort) this.#state = next;
	}

	async #run(positions: readonly number[], abort: AbortController): Promise<State> {
		const { signal } = abort;
		const awake = new AbortController();
		void keepAwake(AbortSignal.any([signal, awake.signal]));
		try {
			const total = positions.length;
			let done = 0;
			let skipped = 0;
			const tasks = positions.map((index) => () => this.#prepare(this.items[index], signal));
			for await (const url of inOrder(tasks, CONCURRENCY)) {
				if (signal.aborted) return { kind: 'idle' };
				if (url) {
					download(url);
					await sleep(DOWNLOAD_GAP_MS, signal);
				} else {
					skipped++;
				}
				done++;
				this.#update(abort, { kind: 'saving', abort, done, total });
			}
			if (skipped === total) return { kind: 'failed', message: DOWNLOADS_FAILED };
			return { kind: 'done', skipped };
		} catch (err) {
			if (signal.aborted) return { kind: 'idle' };
			throw err;
		} finally {
			awake.abort();
		}
	}

	/** The URL of the item's best variant once it's ready; null if the server can't provide one. */
	async #prepare(item: ShareItem, signal: AbortSignal): Promise<string | null> {
		const { kind, ref } = this;
		const variants = saveVariants(item).map((variant) =>
			preparer({ kind, ref, id: item.id, variant })
		);
		try {
			return await withRetries(async () => {
				for (const prepare of variants) {
					const result = await prepare(signal);
					if (result.kind === 'ready') return result.url;
					if (result.message !== NOT_AVAILABLE) throw new Error(result.message);
				}
				return null;
			}, signal);
		} catch {
			return null;
		}
	}
}
