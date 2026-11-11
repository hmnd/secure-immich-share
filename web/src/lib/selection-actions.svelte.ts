import type { LinkKind } from '#lib/api/index.ts';
import { download } from '#lib/download.ts';
import { MediaLoader, type MediaState } from '#lib/media-loader.svelte.ts';
import type { SaveJob } from '#lib/save-job.ts';
import { saveVariants } from '#lib/saving.ts';
import type { Selection } from '#lib/selection.svelte.ts';
import { ZipJob, type ZipState } from '#lib/zip-job.svelte.ts';

/** A download started from the selection: a zip of several items, or one item's file. */
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

/**
 * What the selection bar does with the selected items: download them (one directly, more as a
 * zip) or save them to the phone. Starting either clears the selection; the bar then shows
 * its progress. Nothing new starts while a download or save is running.
 */
export class SelectionActions {
	readonly kind: LinkKind;
	readonly ref: string;
	readonly selection: Selection;
	/** Null when this device can't save to the phone. */
	readonly saveJob: SaveJob | null;
	#download = $state.raw<Download>({ kind: 'none' });

	constructor(kind: LinkKind, ref: string, selection: Selection, saveJob: SaveJob | null) {
		this.kind = kind;
		this.ref = ref;
		this.selection = selection;
		this.saveJob = saveJob;
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

	get downloadLabel(): string {
		return this.selection.count === 1 ? 'Download' : 'Download zip';
	}

	/** Downloads the selected item, or a zip of the selected items. */
	startDownload(): void {
		const indices = this.selection.indices;
		if (this.busy || indices.length === 0) return;
		if (indices.length === 1) {
			const item = this.selection.items[indices[0]];
			const loader = new MediaLoader({
				kind: this.kind,
				ref: this.ref,
				id: item.id,
				variant: saveVariants(item)[0]
			});
			const next: Download = { kind: 'file', loader };
			this.#download = next;
			void loader.start().then((state) => {
				if (state.kind === 'ready' && this.#download === next) download(state.url);
			});
		} else {
			const job = new ZipJob(this.kind, this.ref, this.selection.ids);
			this.#download = { kind: 'zip', job };
			job.start();
		}
		this.selection.clear();
	}

	/** Saves the selected items to the phone. Call from a tap handler. */
	save(): void {
		const indices = this.selection.indices;
		if (!this.saveJob || this.busy || indices.length === 0) return;
		this.saveJob.save(indices);
		this.selection.clear();
	}

	/** Hides a finished or failed download, stopping it if it still runs. */
	dismissDownload(): void {
		this.#download = { kind: 'none' };
	}
}
