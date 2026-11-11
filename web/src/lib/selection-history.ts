import { photoParam } from '#lib/photo-url.ts';
import type { Selection } from '#lib/selection.svelte.ts';

/** The selection's history entry, which Back (or the back gesture) pops to clear it. */
type Entry =
	/** No entry: nothing is selected, or the selection started while a photo was open. */
	| { kind: 'none' }
	/** The entry is the current one or below it; going back past it clears the selection. */
	| { kind: 'pushed' }
	/** Going back to drop the entry of a selection that was cleared another way. */
	| { kind: 'popping' };

const isEntry = (state: unknown): boolean =>
	typeof state === 'object' && state !== null && 'selecting' in state;

/** The viewer's photos have their own entries (?photo=); the selection's goes below them. */
const photoOpen = () => photoParam() !== null;

/** Gives a non-empty selection a history entry, so going back clears it instead of leaving. */
export class SelectionHistory {
	readonly #selection: Selection;
	#entry: Entry = { kind: 'none' };

	constructor(selection: Selection) {
		this.#selection = selection;
		selection.onActiveChange(() => this.sync());
		window.addEventListener('popstate', () => this.#popstate());
		// A reload keeps the entry but not the selection.
		if (isEntry(history.state)) {
			this.#entry = { kind: 'pushed' };
			this.sync();
		}
	}

	/** Adds or drops the entry to match the selection. Also call once a photo closes. */
	sync(): void {
		const active = this.#selection.active;
		switch (this.#entry.kind) {
			case 'none':
				if (active && !photoOpen()) {
					history.pushState({ selecting: true }, '', location.href);
					this.#entry = { kind: 'pushed' };
				}
				return;
			case 'pushed':
				// Under an open photo's entry, it's dropped once going back reaches it.
				if (!active && isEntry(history.state)) {
					this.#entry = { kind: 'popping' };
					history.back();
				}
				return;
			case 'popping':
				return;
			default:
				this.#entry satisfies never;
		}
	}

	#popstate(): void {
		switch (this.#entry.kind) {
			case 'none':
				break;
			case 'pushed':
				if (!isEntry(history.state) && !photoOpen()) {
					this.#entry = { kind: 'none' };
					this.#selection.clear();
					return;
				}
				break;
			case 'popping':
				this.#entry = { kind: 'none' };
				break;
			default:
				this.#entry satisfies never;
		}
		this.sync();
	}
}
