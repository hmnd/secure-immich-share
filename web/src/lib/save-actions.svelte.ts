import type { LinkKind, ShareView } from '#lib/api/index.ts';
import { download } from '#lib/download.ts';
import { plural } from '#lib/format.ts';
import { MediaLoader, type MediaState } from '#lib/media-loader.svelte.ts';
import type { SaveJob } from '#lib/save-job.ts';
import { saveVariants } from '#lib/saving.ts';
import type { Selection } from '#lib/selection.svelte.ts';
import { ZipJob, type ZipState } from '#lib/zip-job.svelte.ts';

/** A way to save: this device's save job, or a download (a zip, or one item's file). */
export type SaveMethod = SaveJob['method'] | 'zip';

/** What a save covers: the whole share or the selected items. */
export type SaveTarget = 'all' | 'selection';

export interface SaveOption {
	method: SaveMethod;
	label: string;
}

/** The ways to save offered once more than one fits. */
export interface Chooser {
	title: string;
	options: SaveOption[];
}

/** A download in progress or finished: a zip of several items, or one item's file. */
type Download =
	{ kind: 'none' } | { kind: 'zip'; job: ZipJob } | { kind: 'file'; loader: MediaLoader };

export type DownloadState =
	{ kind: 'none' } | { kind: 'zip'; state: ZipState } | { kind: 'file'; state: MediaState };

function running(state: DownloadState): boolean {
	switch (state.kind) {
		case 'none':
			return false;
		case 'zip':
			return (
				state.state.kind === 'starting' ||
				state.state.kind === 'waiting' ||
				state.state.kind === 'preparing'
			);
		case 'file':
			return state.state.kind === 'checking' || state.state.kind === 'preparing';
		default:
			return state satisfies never;
	}
}

/** `one`: a single item, which downloads as itself rather than a zip. */
function optionLabel(method: SaveMethod, one: boolean): string {
	switch (method) {
		case 'folder':
			return 'Save to a folder';
		case 'share-sheet':
			return 'Save to Photos';
		case 'downloads':
			return 'Download one by one';
		case 'zip':
			return one ? 'Download' : 'Download as zip';
		default:
			return method satisfies never;
	}
}

/**
 * Saving the whole share or the selection. With more than one way to save, the top bar offers
 * a chooser first; with one, it starts straight away. Starting clears the selection, and the bar
 * then shows the progress. Nothing new starts while a save or download is running.
 */
export class SaveActions {
	readonly kind: LinkKind;
	readonly ref: string;
	readonly share: ShareView;
	/** Null when nothing in the share can be selected. */
	readonly selection: Selection | null;
	/** Null when this device can only download. */
	readonly saveJob: SaveJob | null;
	readonly #saveable: number;
	#download = $state.raw<Download>({ kind: 'none' });
	#choosing = $state<SaveTarget | null>(null);

	constructor(
		kind: LinkKind,
		ref: string,
		share: ShareView,
		selection: Selection | null,
		saveJob: SaveJob | null
	) {
		this.kind = kind;
		this.ref = ref;
		this.share = share;
		this.selection = selection;
		this.saveJob = saveJob;
		this.#saveable = share.items.filter((item) => saveVariants(item).length > 0).length;
		// Selecting replaces a chooser for the whole share; emptying the selection closes its own.
		selection?.onChange(() => {
			const choosing = this.#choosing;
			if (choosing === 'all' || (choosing === 'selection' && !selection.active)) {
				this.#choosing = null;
			}
		});
	}

	/** The latest download; reading it keeps a running one going. */
	get download(): DownloadState {
		const current = this.#download;
		switch (current.kind) {
			case 'none':
				return current;
			case 'zip':
				return { kind: 'zip', state: current.job.state };
			case 'file':
				return { kind: 'file', state: current.loader.state };
			default:
				return current satisfies never;
		}
	}

	get busy(): boolean {
		return (this.saveJob?.busy ?? false) || running(this.download);
	}

	get chooser(): Chooser | null {
		const target = this.#choosing;
		if (!target) return null;
		const count = this.#count(target);
		if (count === 0) return null;
		return {
			title: `Save ${plural(count, 'item', 'items')}`,
			options: this.#methods(target).map((method) => ({
				method,
				label: optionLabel(method, target === 'selection' && count === 1)
			}))
		};
	}

	/** Whether there's anything in `target` to save and a way to save it. */
	available(target: SaveTarget): boolean {
		return this.#count(target) > 0 && this.#methods(target).length > 0;
	}

	/** The button label for saving `target`: the way to save when there's only one download. */
	label(target: SaveTarget): string {
		const methods = this.#methods(target);
		if (methods.length === 1 && methods[0] === 'zip') {
			return target === 'all' ? 'Download all' : optionLabel('zip', this.#count('selection') === 1);
		}
		return target === 'all' ? 'Save all' : 'Save';
	}

	/** Saves `target` the one way there is, or opens the chooser. Call from a tap handler. */
	save(target: SaveTarget): void {
		if (this.busy || this.#count(target) === 0) return;
		const methods = this.#methods(target);
		if (methods.length === 1) this.#start(methods[0], target);
		else if (methods.length > 1) this.#choosing = target;
	}

	/** Saves what the chooser offered with `method`. Call from a tap handler. */
	choose(method: SaveMethod): void {
		const target = this.#choosing;
		this.#choosing = null;
		if (target) this.#start(method, target);
	}

	closeChooser(): void {
		this.#choosing = null;
	}

	/** Stops the save, or hides its outcome. */
	closeSave(): void {
		this.saveJob?.cancel();
		this.saveJob?.dismiss();
	}

	/** Stops the download, or hides its outcome. */
	closeDownload(): void {
		this.#download = { kind: 'none' };
	}

	#count(target: SaveTarget): number {
		return target === 'all' ? this.#saveable : (this.selection?.count ?? 0);
	}

	#methods(target: SaveTarget): SaveMethod[] {
		const zip = target === 'all' ? this.share.zipAvailable : this.selection !== null;
		// One item downloads as itself, the same as downloading it one by one.
		const single = target === 'selection' && this.#count(target) === 1;
		const methods: SaveMethod[] = [];
		const job = this.saveJob;
		if (job && !(zip && single && job.method === 'downloads')) methods.push(job.method);
		if (zip) methods.push('zip');
		return methods;
	}

	#start(method: SaveMethod, target: SaveTarget): void {
		if (this.busy) return;
		const indices = target === 'selection' ? this.selection?.indices : undefined;
		if (target === 'selection' && !indices?.length) return;
		this.closeSave();
		this.closeDownload();
		if (method === 'zip') this.#startDownload(indices);
		else this.saveJob?.save(indices);
		if (target === 'selection') this.selection?.clear();
	}

	/** Downloads one item directly, or several (all without `indices`) as a zip. */
	#startDownload(indices: readonly number[] | undefined): void {
		const { kind, ref, share } = this;
		if (indices?.length === 1) {
			const item = share.items[indices[0]];
			const loader = new MediaLoader({ kind, ref, id: item.id, variant: saveVariants(item)[0] });
			const next: Download = { kind: 'file', loader };
			this.#download = next;
			void loader.start().then((state) => {
				if (state.kind === 'ready' && this.#download === next) download(state.url);
			});
			return;
		}
		const ids = indices?.map((index) => share.items[index].id);
		const job = new ZipJob(kind, ref, ids);
		this.#download = { kind: 'zip', job };
		job.start();
	}
}
