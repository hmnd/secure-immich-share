import { mount } from 'svelte';
import TileSelect from '#lib/components/TileSelect.svelte';
import type { Selection } from '#lib/selection.svelte.ts';

const LONG_PRESS_MS = 450;
/** Movement that turns a press into a scroll. */
const SLOP_PX = 10;
/** How close to the screen's bottom, or the sticky bar's, a drag starts scrolling. */
const EDGE_PX = 64;
const MAX_SCROLL_PX = 16;
/** How long after a drag its click may still arrive. */
const CLICK_AFTER_MS = 800;
const STICKY_BAR = '.top-bar';

function tileIndex(target: EventTarget | null): number | null {
	const tile = target instanceof Element ? target.closest<HTMLElement>('.tile') : null;
	return tile?.dataset.index ? Number(tile.dataset.index) : null;
}

function findTouch(list: TouchList, id: number): Touch | null {
	return Array.from(list).find((touch) => touch.identifier === id) ?? null;
}

/** Pixels to scroll per frame for a finger at `y`: faster the deeper it is in an edge zone. */
function scrollSpeed(y: number): number {
	const top = document.querySelector(STICKY_BAR)?.getBoundingClientRect().bottom ?? 0;
	const bottom = window.innerHeight;
	const depth = (into: number) => MAX_SCROLL_PX * Math.min(1, into / EDGE_PX);
	if (y > bottom - EDGE_PX) return depth(y - (bottom - EDGE_PX));
	if (y < top + EDGE_PX) return -depth(top + EDGE_PX - y);
	return 0;
}

type Gesture =
	| { kind: 'idle' }
	/** A finger rests on a selectable tile; holding it still starts a range. */
	| { kind: 'pressing'; id: number; x: number; y: number; index: number; timer: number }
	/** Dragging a range to the tile under the finger; `frame` is a pending auto-scroll step. */
	| { kind: 'dragging'; id: number; x: number; y: number; frame: number | null }
	/** A range just ended: the click that follows it is swallowed. */
	| { kind: 'released'; at: number };

/**
 * Long-press-and-drag range selection on touch screens. Moving before the long press leaves the
 * touch to the browser, so scrolling works as usual; once dragging, the page scrolls itself
 * near the edges.
 */
class TouchRange {
	readonly #grid: HTMLElement;
	readonly #selection: Selection;
	#gesture: Gesture = { kind: 'idle' };

	constructor(grid: HTMLElement, selection: Selection) {
		this.#grid = grid;
		this.#selection = selection;
		grid.addEventListener('touchstart', (event) => this.#start(event), { passive: true });
		grid.addEventListener('touchmove', (event) => this.#move(event), { passive: false });
		grid.addEventListener('touchend', (event) => this.#end(event));
		grid.addEventListener('touchcancel', (event) => this.#end(event));
		grid.addEventListener('contextmenu', (event) => this.#contextmenu(event));
	}

	/** Whether `event` is the click that ends a range, which mustn't toggle or open a tile. */
	swallows(event: MouseEvent): boolean {
		const gesture = this.#gesture;
		if (gesture.kind !== 'released') return false;
		this.#to({ kind: 'idle' });
		return event.timeStamp - gesture.at < CLICK_AFTER_MS;
	}

	/** Moves to another kind of gesture, releasing the current one's timer or frame. */
	#to(next: Gesture): void {
		const current = this.#gesture;
		if (current.kind === 'pressing') clearTimeout(current.timer);
		if (current.kind === 'dragging') {
			if (current.frame !== null) cancelAnimationFrame(current.frame);
			this.#selection.endRange();
		}
		this.#gesture = next;
	}

	#start(event: TouchEvent): void {
		const touch = event.changedTouches[0];
		const index = tileIndex(event.target);
		if (
			event.touches.length !== 1 ||
			!touch ||
			index === null ||
			!this.#selection.selectable(index)
		) {
			this.#to({ kind: 'idle' });
			return;
		}
		const timer = window.setTimeout(() => this.#longPress(), LONG_PRESS_MS);
		const { identifier: id, clientX: x, clientY: y } = touch;
		this.#to({ kind: 'pressing', id, x, y, index, timer });
	}

	#longPress(): void {
		const gesture = this.#gesture;
		if (gesture.kind !== 'pressing') return;
		if (!this.#selection.beginRange(gesture.index)) {
			this.#to({ kind: 'idle' });
			return;
		}
		navigator.vibrate?.(10);
		const { id, x, y } = gesture;
		this.#to({ kind: 'dragging', id, x, y, frame: null });
	}

	#move(event: TouchEvent): void {
		const gesture = this.#gesture;
		if (gesture.kind !== 'pressing' && gesture.kind !== 'dragging') return;
		const touch = findTouch(event.changedTouches, gesture.id);
		if (!touch) return;
		const { clientX: x, clientY: y } = touch;
		if (gesture.kind === 'pressing') {
			if (Math.hypot(x - gesture.x, y - gesture.y) > SLOP_PX) this.#to({ kind: 'idle' });
			return;
		}
		event.preventDefault();
		this.#gesture = { ...gesture, x, y };
		this.#track();
	}

	#end(event: TouchEvent): void {
		const gesture = this.#gesture;
		if (gesture.kind !== 'pressing' && gesture.kind !== 'dragging') return;
		if (!findTouch(event.changedTouches, gesture.id)) return;
		if (gesture.kind === 'pressing') {
			this.#to({ kind: 'idle' });
			return;
		}
		if (event.cancelable) event.preventDefault();
		this.#to({ kind: 'released', at: event.timeStamp });
	}

	/**
	 * Android opens the link menu on a long press, sometimes only after the touch was cancelled
	 * or lifted; on a selectable tile it starts a range instead. Mouse menus are left alone.
	 */
	#contextmenu(event: MouseEvent): void {
		const gesture = this.#gesture;
		const index = tileIndex(event.target);
		const fromTouch =
			gesture.kind === 'pressing' ||
			gesture.kind === 'dragging' ||
			(event instanceof PointerEvent && event.pointerType === 'touch');
		if (!fromTouch || index === null || !this.#selection.selectable(index)) return;
		event.preventDefault();
		if (gesture.kind === 'pressing') this.#longPress();
	}

	/** Extends the range to the tile under the finger and auto-scrolls near the edges. */
	#track(): void {
		const gesture = this.#gesture;
		if (gesture.kind !== 'dragging') return;
		const under = document.elementFromPoint(gesture.x, gesture.y);
		const index = under && this.#grid.contains(under) ? tileIndex(under) : null;
		if (index !== null) this.#selection.extendRange(index);
		if (gesture.frame === null && scrollSpeed(gesture.y) !== 0) {
			this.#gesture = { ...gesture, frame: requestAnimationFrame(() => this.#scroll()) };
		}
	}

	#scroll(): void {
		const gesture = this.#gesture;
		if (gesture.kind !== 'dragging') return;
		this.#gesture = { ...gesture, frame: null };
		const speed = scrollSpeed(gesture.y);
		if (speed === 0) return;
		window.scrollBy(0, speed);
		this.#track();
	}
}

/**
 * Adds selection to the grid's tiles: a checkbox on each selectable one, shift-click ranges
 * with a hover preview, long-press-and-drag ranges on touch screens, and Escape to clear. While
 * anything is selected, a click on a tile toggles it and its zoom button opens it. Handled
 * clicks are default-prevented.
 */
export function enhanceSelection(
	grid: HTMLElement,
	selection: Selection,
	open: (index: number) => void
): void {
	for (const tile of grid.querySelectorAll<HTMLElement>('.tile')) {
		const index = Number(tile.dataset.index);
		if (!selection.selectable(index)) continue;
		// Chrome on Android turns a long press on a draggable link into a drag, cancelling the touch.
		for (const el of tile.querySelectorAll('a, img')) el.setAttribute('draggable', 'false');
		mount(TileSelect, { target: tile, props: { selection, index, onopen: open } });
	}

	const touch = new TouchRange(grid, selection);
	grid.addEventListener('click', (event) => {
		if (touch.swallows(event)) {
			event.preventDefault();
			return;
		}
		if (event.defaultPrevented || event.button !== 0) return;
		if (event.metaKey || event.ctrlKey || event.altKey) return;
		if (event.target instanceof Element && event.target.closest('button')) return;
		const index = tileIndex(event.target);
		if (index === null || !selection.selectable(index)) return;
		if (event.shiftKey && selection.anchor !== null) {
			event.preventDefault();
			selection.selectRange(index);
		} else if (selection.active && !event.shiftKey) {
			event.preventDefault();
			selection.toggle(index);
		}
	});

	// Hover effects follow the input in use rather than (hover: hover), which some touch screens
	// report: phones keep :hover after a tap, and iOS turns a tap that reveals content into a hover.
	const pointer = (event: PointerEvent) => {
		if (grid.dataset.pointer !== event.pointerType) grid.dataset.pointer = event.pointerType;
	};
	grid.addEventListener('pointerdown', pointer);
	grid.addEventListener('pointerover', (event) => {
		pointer(event);
		if (event.pointerType !== 'mouse') return;
		selection.holdShift(event.shiftKey);
		selection.point(tileIndex(event.target));
	});
	grid.addEventListener('pointerleave', () => selection.point(null));

	window.addEventListener('keydown', (event) => {
		if (event.key === 'Shift') {
			selection.holdShift(true);
		} else if (
			event.key === 'Escape' &&
			selection.active &&
			!document.querySelector('dialog[open]')
		) {
			event.preventDefault();
			selection.clear();
		}
	});
	window.addEventListener('keyup', (event) => {
		if (event.key === 'Shift') selection.holdShift(false);
	});
	window.addEventListener('blur', () => selection.holdShift(false));
}
