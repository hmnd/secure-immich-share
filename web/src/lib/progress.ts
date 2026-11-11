import { fileSummary, plural } from '#lib/format.ts';
import type { MediaState } from '#lib/media-loader.svelte.ts';
import type { SaveAllState } from '#lib/save-all.svelte.ts';
import type { SaveDownloadsState } from '#lib/save-downloads.svelte.ts';
import type { SaveFolderState } from '#lib/save-folder.svelte.ts';
import type { SaveJob } from '#lib/save-job.ts';
import type { ZipState } from '#lib/zip-job.svelte.ts';

export interface Progress {
	done: number;
	total: number;
}

/** How the top bar shows a save or download. */
export type Banner =
	/**
	 * Working on its own; `progress` null while it can't be measured. `resumes`: it waits out
	 * a lost connection and carries on.
	 */
	| { kind: 'working'; title: string; progress: Progress | null; resumes: boolean }
	/** A batch is ready and needs a tap to open the share sheet. */
	| { kind: 'waiting'; title: string; action: string; progress: Progress }
	| { kind: 'done'; title: string; detail: string | null }
	| { kind: 'failed'; title: string };

const DOWNLOAD_STARTED = 'Download started';

const skippedNote = (skipped: number, outcome: string) =>
	skipped > 0 ? `${plural(skipped, 'item', 'items')} couldn’t be ${outcome}.` : null;

function folderBanner(state: SaveFolderState): Banner | null {
	switch (state.kind) {
		case 'idle':
			return null;
		case 'picking':
			return {
				kind: 'working',
				title: 'Choose a folder to save to',
				progress: null,
				resumes: true
			};
		case 'saving':
			return {
				kind: 'working',
				title: `Saving ${state.done} of ${state.total}`,
				progress: state,
				resumes: true
			};
		case 'done':
			return {
				kind: 'done',
				title: `Saved to ${state.path}`,
				detail: skippedNote(state.skipped, 'saved')
			};
		case 'failed':
			return { kind: 'failed', title: state.message };
		default:
			return state satisfies never;
	}
}

function batchBanner(state: SaveAllState): Banner | null {
	switch (state.kind) {
		case 'idle':
			return null;
		case 'loading':
			return {
				kind: 'working',
				title: `Getting photos ready: ${state.done} of ${state.total}`,
				progress: state,
				resumes: true
			};
		case 'ready': {
			const { batch, total, only } = state;
			const range = batch.from === batch.to ? `${batch.from}` : `${batch.from}–${batch.to}`;
			return {
				kind: 'waiting',
				title: only ? `${fileSummary(batch.files)} ready` : `${range} of ${total} ready`,
				action: `Save ${fileSummary(batch.files)}`,
				progress: { done: batch.from - 1, total }
			};
		}
		case 'done':
			return { kind: 'done', title: 'All done', detail: skippedNote(state.skipped, 'saved') };
		case 'failed':
			return { kind: 'failed', title: state.message };
		default:
			return state satisfies never;
	}
}

function downloadsBanner(state: SaveDownloadsState): Banner | null {
	switch (state.kind) {
		case 'idle':
			return null;
		case 'saving':
			return {
				kind: 'working',
				title: `Downloading ${state.done} of ${state.total}`,
				progress: state,
				resumes: true
			};
		case 'done':
			return {
				kind: 'done',
				title: 'Saved to Downloads',
				detail: skippedNote(state.skipped, 'downloaded')
			};
		case 'failed':
			return { kind: 'failed', title: state.message };
		default:
			return state satisfies never;
	}
}

export function saveBanner(job: SaveJob): Banner | null {
	switch (job.method) {
		case 'folder':
			return folderBanner(job.state);
		case 'share-sheet':
			return batchBanner(job.state);
		case 'downloads':
			return downloadsBanner(job.state);
		default:
			return job satisfies never;
	}
}

export function zipBanner(state: ZipState): Banner | null {
	switch (state.kind) {
		case 'idle':
			return null;
		case 'starting':
			return { kind: 'working', title: 'Preparing zip…', progress: null, resumes: false };
		case 'waiting':
			return { kind: 'working', title: 'Waiting for the server…', progress: null, resumes: false };
		case 'preparing':
			return {
				kind: 'working',
				title: `Preparing zip: ${state.done} of ${state.total}`,
				progress: state,
				resumes: false
			};
		case 'ready':
			return {
				kind: 'done',
				title: DOWNLOAD_STARTED,
				detail: skippedNote(state.skipped, 'included')
			};
		case 'failed':
			return { kind: 'failed', title: state.message };
		default:
			return state satisfies never;
	}
}

/** One item's download: preparing its file, then downloading it. */
export function fileBanner(state: MediaState): Banner | null {
	switch (state.kind) {
		case 'idle':
			return null;
		case 'checking':
		case 'preparing':
			return { kind: 'working', title: 'Preparing download…', progress: null, resumes: false };
		case 'ready':
			return { kind: 'done', title: DOWNLOAD_STARTED, detail: null };
		case 'failed':
			return { kind: 'failed', title: state.message };
		default:
			return state satisfies never;
	}
}
