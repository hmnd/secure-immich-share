import type { ShareItem } from '#lib/api/index.ts';
import { photoParam, photoURL } from '#lib/photo-url.ts';

export type View =
	| { kind: 'closed' }
	/** `pushed`: the photo's history entry sits on top of the grid's. */
	| { kind: 'open'; index: number; pushed: boolean }
	/** Going back past a pushed photo's entry; closed once the browser gets there. */
	| { kind: 'closing' };

/** Which photo the viewer shows, kept in step with the URL (?photo=) and history. */
export class PhotoView {
	readonly items: readonly ShareItem[];
	readonly #onclosed: () => void;
	#view = $state.raw<View>({ kind: 'closed' });

	/**
	 * `initial`: the photo the page was loaded with. `onclosed` runs once the viewer has closed
	 * and the history is back on the gallery.
	 */
	constructor(items: readonly ShareItem[], initial: number | null, onclosed: () => void) {
		this.items = items;
		this.#onclosed = onclosed;
		if (initial !== null && items[initial]) {
			this.#view = { kind: 'open', index: initial, pushed: false };
		}
	}

	get current(): View {
		return this.#view;
	}

	/** Whether the gallery is showing, with no photo open or closing. */
	get isClosed(): boolean {
		return this.#view.kind === 'closed';
	}

	open(index: number): void {
		const item = this.items[index];
		if (this.#view.kind !== 'closed' || !item) return;
		history.pushState({ photo: true }, '', photoURL(item.id));
		this.#view = { kind: 'open', index, pushed: true };
	}

	show(index: number): void {
		const view = this.#view;
		const item = this.items[index];
		if (view.kind !== 'open' || !item) return;
		this.#view = { ...view, index };
		history.replaceState(history.state, '', photoURL(item.id));
	}

	close(): void {
		const view = this.#view;
		if (view.kind !== 'open') return;
		if (view.pushed) {
			this.#view = { kind: 'closing' };
			history.back();
			return;
		}
		this.#view = { kind: 'closed' };
		history.replaceState(null, '', location.pathname);
		const id = this.items[view.index].id;
		document.getElementById(`p-${id}`)?.scrollIntoView({ block: 'center' });
		this.#onclosed();
	}

	/** Follows the URL after Back or Forward. */
	popstate(): void {
		const was = this.#view.kind;
		const photo = photoParam();
		const index = photo === null ? -1 : this.items.findIndex((item) => item.id === photo);
		const next: View =
			index === -1
				? { kind: 'closed' }
				: {
						kind: 'open',
						index,
						pushed: Boolean((history.state as { photo?: unknown } | null)?.photo)
					};
		this.#view = next;
		if (was !== 'closed' && next.kind === 'closed') this.#onclosed();
	}
}
