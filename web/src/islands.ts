// The server renders every page (server/internal/pages) and they all work
// without JavaScript. This entry adds what needs it to a gallery page: the
// lightbox, selection, downloads and saving, and the measured grid layout.
import './app.css';
import './pages.css';
import { mount } from 'svelte';
import type { LinkKind, ShareView } from '#lib/api/index.ts';
import ShareActions from '#lib/components/ShareActions.svelte';
import TopBar from '#lib/components/TopBar.svelte';
import Viewer from '#lib/components/Viewer.svelte';
import { enhanceGrid } from '#lib/grid.ts';
import { PhotoView } from '#lib/photo-view.svelte.ts';
import { createSaveJob } from '#lib/save-job.ts';
import { enhanceSelection } from '#lib/select-grid.ts';
import { SelectionActions } from '#lib/selection-actions.svelte.ts';
import { SelectionHistory } from '#lib/selection-history.ts';
import { Selection } from '#lib/selection.svelte.ts';
import { ZipJob } from '#lib/zip-job.svelte.ts';

/** Written by the server as #share-data (pages.islandData). */
interface PageData {
	kind: LinkKind;
	ref: string;
	share: ShareView;
	/** The open photo's index. */
	photo: number | null;
}

function pageData(): PageData | null {
	const text = document.getElementById('share-data')?.textContent;
	return text ? (JSON.parse(text) as PageData) : null;
}

function island(name: string): Element | null {
	return document.querySelector(`[data-island="${name}"]`);
}

const data = pageData();

if (data) {
	const { kind, ref, share } = data;
	const saveJob = createSaveJob(kind, ref, share);
	const zip = share.zipAvailable ? new ZipJob(kind, ref) : null;
	const selection = Selection.of(share);
	const selectionHistory = selection && new SelectionHistory(selection);
	const photos = new PhotoView(share.items, data.photo, () => selectionHistory?.sync());

	const banner = island('banner');
	if (banner && (selection || saveJob)) {
		const actions = selection && new SelectionActions(kind, ref, selection, saveJob);
		mount(TopBar, { target: banner, props: { actions, saveJob, photos } });
	}
	const actions = island('actions');
	if (actions) mount(ShareActions, { target: actions, props: { zip, saveJob } });

	document.getElementById('viewer')?.remove();
	mount(Viewer, { target: document.body, props: { kind, ref, photos, selection } });

	const grid = document.querySelector<HTMLElement>('.grid');
	if (grid) {
		enhanceGrid(grid);
		if (selection) enhanceSelection(grid, selection, (index) => photos.open(index));
		grid.addEventListener('click', (event) => {
			if (event.defaultPrevented || event.button !== 0) return;
			if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
			const open = event.target instanceof Element ? event.target.closest('.tile .open') : null;
			const tile = open?.closest<HTMLElement>('.tile');
			if (!tile?.dataset.index) return;
			event.preventDefault();
			photos.open(Number(tile.dataset.index));
		});
	}
}
