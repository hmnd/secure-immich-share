import { SvelteSet } from 'svelte/reactivity';
import type { ShareItem, ShareView } from '#lib/api/index.ts';
import { saveVariants } from '#lib/saving.ts';

/** Whether a range selects its items or deselects them. */
export type RangeMode = 'add' | 'remove';

/** Positions `from`..`to` (inclusive, in share order) that a range would change. */
export interface Span {
	from: number;
	to: number;
	mode: RangeMode;
}

/** A range being dragged out from `origin`; `base` is whether each item was selected before. */
type Range =
	{ kind: 'idle' } | { kind: 'ranging'; origin: number; mode: RangeMode; base: readonly boolean[] };

/** The items picked for downloading or saving, by share position. */
export class Selection {
	readonly items: readonly ShareItem[];
	readonly #selectable: readonly boolean[];
	readonly #selected = new SvelteSet<number>();
	/** The last item toggled: where shift-click ranges start. */
	#anchor = $state<number | null>(null);
	/** The tile under the mouse, and whether Shift is held: they preview a shift-click. */
	#pointer = $state<number | null>(null);
	#shift = $state(false);
	#range: Range = { kind: 'idle' };
	#onactivechange: ((active: boolean) => void) | null = null;

	readonly #preview = $derived.by((): Span | null => {
		const anchor = this.#anchor;
		const pointer = this.#pointer;
		if (!this.#shift || anchor === null || pointer === null) return null;
		return {
			from: Math.min(anchor, pointer),
			to: Math.max(anchor, pointer),
			mode: this.#selected.has(pointer) ? 'remove' : 'add'
		};
	});

	/** A selection for the share, or null when nothing in it can be downloaded. */
	static of(share: ShareView): Selection | null {
		if (!share.allowDownload) return null;
		const selection = new Selection(share.items);
		return selection.#selectable.includes(true) ? selection : null;
	}

	private constructor(items: readonly ShareItem[]) {
		this.items = items;
		this.#selectable = items.map((item) => saveVariants(item).length > 0);
	}

	/** Calls `listener` whenever the selection goes from empty to not, or back. */
	onActiveChange(listener: (active: boolean) => void): void {
		this.#onactivechange = listener;
	}

	get count(): number {
		return this.#selected.size;
	}

	get active(): boolean {
		return this.#selected.size > 0;
	}

	get anchor(): number | null {
		return this.#anchor;
	}

	/** Selected positions in share order. */
	get indices(): number[] {
		return [...this.#selected].sort((a, b) => a - b);
	}

	/** Selected item ids in share order. */
	get ids(): string[] {
		return this.indices.map((index) => this.items[index].id);
	}

	/** What a shift-click on the tile under the mouse would change. */
	get preview(): Span | null {
		return this.#preview;
	}

	selectable(index: number): boolean {
		return this.#selectable[index] ?? false;
	}

	has(index: number): boolean {
		return this.#selected.has(index);
	}

	/** Whether the shift-click preview would change this item. */
	candidate(index: number): boolean {
		const span = this.#preview;
		if (!span || index < span.from || index > span.to || !this.selectable(index)) return false;
		return this.#selected.has(index) === (span.mode === 'remove');
	}

	toggle(index: number): void {
		if (!this.selectable(index)) return;
		this.#change(() => {
			if (this.#selected.has(index)) this.#selected.delete(index);
			else this.#selected.add(index);
		});
		this.#anchor = index;
	}

	/**
	 * Selects from the anchor to `index`, or deselects when `index` is already selected, then
	 * moves the anchor there. Without an anchor it toggles `index`.
	 */
	selectRange(index: number): void {
		const anchor = this.#anchor;
		if (anchor === null) {
			this.toggle(index);
			return;
		}
		const add = !this.#selected.has(index);
		this.#change(() => {
			for (let i = Math.min(anchor, index); i <= Math.max(anchor, index); i++) {
				if (!this.selectable(i)) continue;
				if (add) this.#selected.add(i);
				else this.#selected.delete(i);
			}
		});
		this.#anchor = index;
	}

	/** Shift-click handling: a range when there's an anchor, otherwise a toggle. */
	pick(index: number, range: boolean): void {
		if (range) this.selectRange(index);
		else this.toggle(index);
	}

	/** Tracks the tile under the mouse for the shift-click preview. */
	point(index: number | null): void {
		this.#pointer = index;
	}

	holdShift(held: boolean): void {
		this.#shift = held;
	}

	/**
	 * Starts dragging a range from `index`: it selects, or deselects when `index` is already
	 * selected. False when `index` can't be selected.
	 */
	beginRange(index: number): boolean {
		if (!this.selectable(index)) return false;
		this.#range = {
			kind: 'ranging',
			origin: index,
			mode: this.#selected.has(index) ? 'remove' : 'add',
			base: this.items.map((_, i) => this.#selected.has(i))
		};
		this.#anchor = index;
		this.extendRange(index);
		return true;
	}

	/** Moves the dragged range's end to `index`; dragging back shrinks it. */
	extendRange(index: number): void {
		const range = this.#range;
		if (range.kind !== 'ranging') return;
		const { origin, mode, base } = range;
		const from = Math.min(origin, index);
		const to = Math.max(origin, index);
		this.#change(() => {
			for (let i = 0; i < this.items.length; i++) {
				const want = i >= from && i <= to && this.selectable(i) ? mode === 'add' : base[i];
				if (want === this.#selected.has(i)) continue;
				if (want) this.#selected.add(i);
				else this.#selected.delete(i);
			}
		});
	}

	endRange(): void {
		this.#range = { kind: 'idle' };
	}

	clear(): void {
		this.#anchor = null;
		this.#range = { kind: 'idle' };
		this.#change(() => this.#selected.clear());
	}

	/** Runs `update` on the selected set, then reports if it became empty or non-empty. */
	#change(update: () => void): void {
		const was = this.#selected.size > 0;
		update();
		const now = this.#selected.size > 0;
		if (was !== now) this.#onactivechange?.(now);
	}
}
