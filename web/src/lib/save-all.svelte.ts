import { createSubscriber } from 'svelte/reactivity';
import { errorMessage, type LinkKind, type ShareItem } from '#lib/api/index.ts';
import { preparer } from '#lib/media-loader.svelte.ts';
import { withRetries } from '#lib/retry.ts';
import { CONCURRENCY, fileName, inOrder, saveVariants } from '#lib/saving.ts';
import { loadShareable, shareFiles, shareLimits, type ShareLimits } from '#lib/share-sheet.ts';

/** Files for one share sheet. `from` and `to` are 1-based positions in the run. */
export interface Batch {
	files: File[];
	from: number;
	to: number;
}

export type SaveAllState =
	| { kind: 'idle' }
	| { kind: 'loading'; done: number; total: number }
	/** `only`: the run fits in this one batch. */
	| { kind: 'ready'; batch: Batch; total: number; only: boolean }
	| { kind: 'done'; skipped: number }
	| { kind: 'failed'; message: string };

interface Run {
	abort: AbortController;
	/** How many items the run saves. */
	total: number;
	/** Batches waiting for a tap, oldest first. */
	queue: Batch[];
	batches: number;
	skipped: number;
}

/** Loading items; `room` wakes the loader once the queue has space for its next batch. */
type Running = Run & { kind: 'running'; done: number; room: (() => void) | null };
/** Everything is loaded; what's left in `queue` still waits for a tap. */
type Finished = Run & { kind: 'finished' };

type State = { kind: 'idle' } | Running | Finished | { kind: 'failed'; message: string };

const SHARE_FAILED = 'Couldn’t open the share menu';

/**
 * Saves items through the share sheet, one batch per tap. The next batch loads while the
 * current one waits; at most one waits at a time. Stops once nothing reads `state`.
 */
export class SaveAllJob {
	readonly method = 'share-sheet';
	readonly kind: LinkKind;
	readonly ref: string;
	readonly items: readonly ShareItem[];
	#state = $state.raw<State>({ kind: 'idle' });
	#view = $derived.by(() => view(this.#state));
	#subscribe: () => void;

	constructor(kind: LinkKind, ref: string, items: readonly ShareItem[]) {
		this.kind = kind;
		this.ref = ref;
		this.items = items;
		this.#subscribe = createSubscriber(() => () => this.cancel());
	}

	get state(): SaveAllState {
		this.#subscribe();
		return this.#view;
	}

	get busy(): boolean {
		const { kind } = this.state;
		return kind === 'loading' || kind === 'ready';
	}

	/**
	 * Starts saving the items at `indices` (every item without them), or opens the share sheet
	 * for the waiting batch. Call from a tap handler.
	 */
	save(indices?: readonly number[]): void {
		const state = this.#state;
		if (state.kind === 'running' || state.kind === 'finished') {
			const batch = state.queue[0];
			if (batch) {
				this.#share(state, batch);
				return;
			}
			if (state.kind === 'running') return;
		}
		const positions = indices ?? this.items.map((_, index) => index);
		const abort = new AbortController();
		this.#state = {
			kind: 'running',
			abort,
			total: positions.length,
			queue: [],
			batches: 0,
			skipped: 0,
			done: 0,
			room: null
		};
		void this.#produce(abort, positions).then(
			() => {
				const run = this.#run(abort);
				if (run?.kind !== 'running') return;
				const { total, queue, batches, skipped } = run;
				this.#state = { kind: 'finished', abort, total, queue, batches, skipped };
			},
			(err: unknown) => {
				if (this.#run(abort)) this.#state = { kind: 'failed', message: errorMessage(err) };
			}
		);
	}

	/** Stops loading and drops waiting batches; a finished save keeps its outcome. */
	cancel(): void {
		const state = this.#state;
		if (state.kind !== 'running' && state.kind !== 'finished') return;
		state.abort.abort();
		if (state.kind === 'running' || state.queue.length > 0) this.#state = { kind: 'idle' };
	}

	/** Hides a finished save's outcome. */
	dismiss(): void {
		const state = this.#state;
		if (state.kind === 'failed' || (state.kind === 'finished' && state.queue.length === 0)) {
			this.#state = { kind: 'idle' };
		}
	}

	/** The run `abort` belongs to, unless it was cancelled or replaced. */
	#run(abort: AbortController): Running | Finished | null {
		const state = this.#state;
		return (state.kind === 'running' || state.kind === 'finished') && state.abort === abort
			? state
			: null;
	}

	/** The loading state of `abort`'s run; throws once that run is over. */
	#running(abort: AbortController): Running {
		abort.signal.throwIfAborted();
		const run = this.#run(abort);
		if (run?.kind !== 'running') throw new Error('The save moved on');
		return run;
	}

	#share(run: Running | Finished, batch: Batch): void {
		const queue = run.queue.slice(1);
		if (run.kind === 'running') {
			this.#state = { ...run, queue, room: null };
			run.room?.();
		} else {
			this.#state = { ...run, queue };
		}
		// Moves on without waiting: Android doesn't always report when the sheet closes.
		void shareFiles(batch.files).then((outcome) => {
			const current = this.#run(run.abort);
			if (!current) return;
			switch (outcome) {
				case 'shared':
					break;
				case 'dismissed':
					this.#state = { ...current, queue: [batch, ...current.queue] };
					break;
				case 'failed':
					current.abort.abort();
					this.#state = { kind: 'failed', message: SHARE_FAILED };
					break;
				default:
					outcome satisfies never;
			}
		});
	}

	async #produce(abort: AbortController, positions: readonly number[]): Promise<void> {
		const { signal } = abort;
		const limits = shareLimits();
		const tasks = positions.map(
			(index) => () => this.#load(this.items[index], index, limits, signal)
		);
		let batch: Batch | null = null;
		let bytes = 0;
		let position = 0;
		for await (const file of inOrder(tasks, CONCURRENCY)) {
			position++;
			const run = this.#running(abort);
			this.#state = { ...run, done: run.done + 1, skipped: run.skipped + (file ? 0 : 1) };
			if (!file) continue;
			if (batch && (batch.files.length >= limits.maxFiles || bytes + file.size > limits.maxBytes)) {
				await this.#push(abort, batch);
				batch = null;
			}
			if (!batch) {
				batch = { files: [], from: position, to: position };
				bytes = 0;
			}
			batch.files.push(file);
			batch.to = position;
			bytes += file.size;
		}
		if (batch) await this.#push(abort, batch);
	}

	/** Loads one item as a file named after its share position; null if it can't be. */
	async #load(
		item: ShareItem,
		index: number,
		limits: ShareLimits,
		signal: AbortSignal
	): Promise<File | null> {
		const { kind, ref } = this;
		const variants = saveVariants(item).map((variant) =>
			preparer({ kind, ref, id: item.id, variant })
		);
		try {
			const loaded = await withRetries(
				() => loadShareable(variants, fileName(index), limits, signal),
				signal
			);
			return loaded.kind === 'file' ? loaded.file : null;
		} catch {
			return null;
		}
	}

	/** Queues `batch` once the previous one has gone to the share sheet. */
	async #push(abort: AbortController, batch: Batch): Promise<void> {
		const { signal } = abort;
		for (;;) {
			const run = this.#running(abort);
			if (run.queue.length === 0) {
				this.#state = { ...run, queue: [batch], batches: run.batches + 1 };
				return;
			}
			await new Promise<void>((resolve, reject) => {
				const onAbort = () => reject(signal.reason);
				signal.addEventListener('abort', onAbort, { once: true });
				this.#state = {
					...run,
					room: () => {
						signal.removeEventListener('abort', onAbort);
						resolve();
					}
				};
			});
		}
	}
}

function view(state: State): SaveAllState {
	switch (state.kind) {
		case 'idle':
		case 'failed':
			return state;
		case 'running': {
			const batch = state.queue[0];
			return batch
				? { kind: 'ready', batch, total: state.total, only: false }
				: { kind: 'loading', done: state.done, total: state.total };
		}
		case 'finished': {
			const batch = state.queue[0];
			return batch
				? { kind: 'ready', batch, total: state.total, only: state.batches === 1 }
				: { kind: 'done', skipped: state.skipped };
		}
		default:
			return state satisfies never;
	}
}
