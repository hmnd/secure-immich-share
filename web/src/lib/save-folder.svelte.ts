import { createSubscriber } from 'svelte/reactivity';
import type { LinkKind, ShareItem } from '#lib/api/index.ts';
import { fetchPrepared, preparer } from '#lib/media-loader.svelte.ts';
import { withRetries } from '#lib/retry.ts';
import { CONCURRENCY, fileName, inOrder, mediaType, saveVariants } from '#lib/saving.ts';

export type SaveFolderState =
	| { kind: 'idle' }
	| { kind: 'picking' }
	| { kind: 'saving'; done: number; total: number }
	/** `path`: where the files went, e.g. "Pictures › Summer 2024". */
	| { kind: 'done'; path: string; skipped: number }
	| { kind: 'failed'; message: string };

type State =
	| { kind: 'idle' }
	| { kind: 'picking'; abort: AbortController }
	| { kind: 'saving'; abort: AbortController; done: number; total: number }
	| Extract<SaveFolderState, { kind: 'done' | 'failed' }>;

const FOLDER_FAILED = 'Couldn’t save to that folder';
const FOLDER_LOST = 'Lost access to the folder. Save again to carry on where it stopped.';
const STORAGE_FULL = 'The phone is out of space.';

/** A folder error that every later item would hit too, so the whole save stops. */
class FolderError extends Error {}

function folderError(err: unknown): FolderError | null {
	if (!(err instanceof DOMException)) return null;
	switch (err.name) {
		case 'QuotaExceededError':
			return new FolderError(STORAGE_FULL);
		case 'NotAllowedError':
		case 'SecurityError':
		case 'NotFoundError':
			return new FolderError(FOLDER_LOST);
		default:
			return null;
	}
}

/** A folder name for the share: its title without characters file systems reject. */
export function folderName(title: string | null): string {
	const name = (title ?? '')
		.replace(/[\\/:*?"<>|\p{Cc}]/gu, ' ')
		.replace(/\s+/g, ' ')
		.replace(/^[\s.]+|[\s.]+$/g, '')
		.slice(0, 100)
		.trim();
	return name || 'Shared photos';
}

/**
 * Saves items into a subfolder, named after the share, of a folder the visitor picks.
 * Files already there with the same size are kept, so a second run resumes. Stops once nothing
 * reads `state`.
 */
export class SaveFolderJob {
	readonly method = 'folder';
	readonly kind: LinkKind;
	readonly ref: string;
	readonly items: readonly ShareItem[];
	readonly folder: string;
	#state = $state.raw<State>({ kind: 'idle' });
	#subscribe: () => void;

	constructor(kind: LinkKind, ref: string, items: readonly ShareItem[], folder: string) {
		this.kind = kind;
		this.ref = ref;
		this.items = items;
		this.folder = folder;
		this.#subscribe = createSubscriber(() => () => this.cancel());
	}

	get state(): SaveFolderState {
		this.#subscribe();
		return this.#state;
	}

	get busy(): boolean {
		const { kind } = this.state;
		return kind === 'picking' || kind === 'saving';
	}

	/**
	 * Opens the folder picker, then saves the items at `indices` (every item without them).
	 * Call from a tap handler.
	 */
	save(indices?: readonly number[]): void {
		const kind = this.#state.kind;
		if (kind === 'picking' || kind === 'saving' || !window.showDirectoryPicker) return;
		const picked = window.showDirectoryPicker({
			id: 'save-to-phone',
			mode: 'readwrite',
			startIn: 'pictures'
		});
		const positions = indices ?? this.items.map((_, index) => index);
		const abort = new AbortController();
		this.#state = { kind: 'picking', abort };
		void this.#run(picked, positions, abort).then(
			(next) => this.#update(abort, next),
			() => this.#update(abort, { kind: 'failed', message: FOLDER_FAILED })
		);
	}

	cancel(): void {
		const state = this.#state;
		if (state.kind !== 'picking' && state.kind !== 'saving') return;
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
		if ((state.kind === 'picking' || state.kind === 'saving') && state.abort === abort) {
			this.#state = next;
		}
	}

	async #run(
		picked: Promise<FileSystemDirectoryHandle>,
		positions: readonly number[],
		abort: AbortController
	): Promise<State> {
		const { signal } = abort;
		let parent: FileSystemDirectoryHandle;
		try {
			parent = await picked;
		} catch (err) {
			if (err instanceof DOMException && err.name === 'AbortError') return { kind: 'idle' };
			return { kind: 'failed', message: FOLDER_FAILED };
		}
		if (signal.aborted) return { kind: 'idle' };
		const awake = new AbortController();
		void keepAwake(AbortSignal.any([signal, awake.signal]));
		// Stops the other workers once one hits a FolderError.
		const stop = new AbortController();
		const running = AbortSignal.any([signal, stop.signal]);
		try {
			const dir = await parent.getDirectoryHandle(this.folder, { create: true });
			const total = positions.length;
			let done = 0;
			let skipped = 0;
			this.#update(abort, { kind: 'saving', abort, done, total });
			const tasks = positions.map((index) => async () => {
				let saved: boolean;
				try {
					saved = await withRetries(
						() => this.#saveItem(dir, this.items[index], index, running),
						running,
						(err) => err instanceof FolderError
					);
				} catch (err) {
					if (err instanceof FolderError) stop.abort(err);
					if (running.aborted) return;
					saved = false;
				}
				done++;
				if (!saved) skipped++;
				this.#update(abort, { kind: 'saving', abort, done, total });
			});
			for await (const _ of inOrder(tasks, CONCURRENCY)) if (running.aborted) break;
			if (signal.aborted) return { kind: 'idle' };
			if (stop.signal.aborted) {
				const reason: unknown = stop.signal.reason;
				return { kind: 'failed', message: reason instanceof Error ? reason.message : FOLDER_LOST };
			}
			if (skipped === total) return { kind: 'failed', message: FOLDER_FAILED };
			return { kind: 'done', path: `${parent.name} › ${this.folder}`, skipped };
		} catch (err) {
			if (signal.aborted) return { kind: 'idle' };
			return { kind: 'failed', message: folderError(err)?.message ?? FOLDER_FAILED };
		} finally {
			awake.abort();
		}
	}

	/**
	 * Saves one item: true once it's in the folder, false if the server can't provide it.
	 * Throws to be retried, or a FolderError to stop the whole save.
	 */
	async #saveItem(
		dir: FileSystemDirectoryHandle,
		item: ShareItem,
		index: number,
		signal: AbortSignal
	): Promise<boolean> {
		const { kind, ref } = this;
		const variants = saveVariants(item).map((variant) =>
			preparer({ kind, ref, id: item.id, variant })
		);
		const fetched = await fetchPrepared(variants, signal, async (res) => {
			const media = mediaType(res.headers.get('content-type'));
			if (!media) return null;
			if (!res.body) throw new Error('Empty response');
			const name = `${fileName(index)}.${media.ext}`;
			const length = res.headers.get('content-length');
			try {
				if (length !== null && (await sizeOf(dir, name)) === Number(length)) {
					await res.body.cancel();
					return true;
				}
				const file = await dir.getFileHandle(name, { create: true });
				await res.body.pipeTo(await file.createWritable(), { signal });
			} catch (err) {
				throw folderError(err) ?? err;
			}
			return true;
		});
		return fetched.kind === 'taken';
	}
}

async function sizeOf(dir: FileSystemDirectoryHandle, name: string): Promise<number | null> {
	try {
		return (await (await dir.getFileHandle(name)).getFile()).size;
	} catch {
		return null;
	}
}

/**
 * Keeps the screen on until `signal` aborts. The browser drops the lock whenever the page is
 * hidden, so it's requested again each time the page comes back.
 */
async function keepAwake(signal: AbortSignal): Promise<void> {
	let lock: WakeLockSentinel | null = null;
	const request = async () => {
		if (signal.aborted || document.visibilityState !== 'visible') return;
		try {
			lock = await navigator.wakeLock.request('screen');
			if (signal.aborted) void lock.release();
		} catch {
			lock = null;
		}
	};
	document.addEventListener('visibilitychange', () => void request(), { signal });
	signal.addEventListener('abort', () => void lock?.release(), { once: true });
	await request();
}
