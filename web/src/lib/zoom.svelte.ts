export interface Point {
	x: number;
	y: number;
}

/** What a pointer went down on. */
export type Hit = 'photo' | 'button' | 'backdrop';

interface View {
	scale: number;
	/** The photo's offset from the centre of its area. */
	x: number;
	y: number;
}

interface Touch {
	at: Point;
	start: Point;
	/** Still a tap on the photo: the only finger down, and it hasn't moved. */
	tap: boolean;
}

type Pinching = { kind: 'pinching'; distance: number; centre: Point };

/** One finger or the mouse pans a zoomed photo; two fingers pinch it. */
type Gesture = { kind: 'idle' } | { kind: 'panning'; id: number; at: Point } | Pinching;

interface Options {
	/** The photo's aspect ratio. */
	ratio: number;
	/** The bounding box the photo is laid out in, as large as fits and centred. */
	area: () => DOMRect | null;
	/** Called each time the photo zooms in from fitted. */
	onzoom: () => void;
}

const FIT: View = { scale: 1, x: 0, y: 0 };
const MAX_SCALE = 8;
/** Where the zoom button and a double tap zoom to. */
const STEP_SCALE = 2.5;
/** Pinched back to under this, the photo springs back to fit. */
const SNAP_SCALE = 1.1;
const TAP_SLOP_PX = 10;
const DOUBLE_TAP_MS = 300;
const DOUBLE_TAP_SLOP_PX = 30;
const WHEEL_LINE_PX = 40;
/** How far a pixel of wheel travel zooms; a trackpad pinch scrolls with ctrlKey set. */
const WHEEL_ZOOM_PER_PX = 0.002;
const PINCH_ZOOM_PER_PX = 0.01;
/** At most one mouse wheel notch's zoom per event, as a mouse wheel with ctrl looks like a pinch. */
const MAX_WHEEL_ZOOM = 100 * WHEEL_ZOOM_PER_PX;

const point = (event: MouseEvent): Point => ({ x: event.clientX, y: event.clientY });
const distance = (a: Point, b: Point) => Math.hypot(b.x - a.x, b.y - a.y);
const centre = (area: DOMRect): Point => ({
	x: area.left + area.width / 2,
	y: area.top + area.height / 2
});
const limit = (value: number, max: number) => (max > 0 ? Math.min(max, Math.max(-max, value)) : 0);

/**
 * Zooms and pans one photo: with the zoom button, a double tap or click, the wheel, or by
 * pinching. The photo always covers as much of its area as it can. Points are client
 * coordinates.
 */
export class Zoom {
	readonly #options: Options;
	#view = $state.raw<View>(FIT);
	#smooth = $state(false);
	#gesture = $state.raw<Gesture>({ kind: 'idle' });
	// eslint-disable-next-line svelte/prefer-svelte-reactivity -- bookkeeping: nothing renders it
	readonly #touches = new Map<number, Touch>();
	#lastTap: (Point & { t: number }) | null = null;

	constructor(options: Options) {
		this.#options = options;
	}

	get scale(): number {
		return this.#view.scale;
	}

	get x(): number {
		return this.#view.x;
	}

	get y(): number {
		return this.#view.y;
	}

	get zoomed(): boolean {
		return this.#view.scale > 1;
	}

	/** Whether the last change eases in, rather than following a gesture. */
	get smooth(): boolean {
		return this.#smooth;
	}

	/** Whether a gesture is zooming or panning the photo: the pointers are the zoom's. */
	get active(): boolean {
		return this.#gesture.kind !== 'idle';
	}

	get panning(): boolean {
		return this.#gesture.kind === 'panning';
	}

	/** Zooms in on `at` (the centre by default), or back out to fit. */
	toggle(at?: Point): void {
		if (this.zoomed) {
			this.#fit();
			return;
		}
		const area = this.#options.area();
		if (!area) return;
		at ??= centre(area);
		this.#set(area, STEP_SCALE, at, at);
		this.#smooth = true;
	}

	/** A pointer went down on `hit`; true once the zoom follows the pointers. */
	down(event: PointerEvent & { currentTarget: Element }, hit: Hit): boolean {
		const at = point(event);
		if (event.pointerType !== 'touch') {
			if (!this.zoomed || event.button !== 0 || hit === 'button') return false;
			event.currentTarget.setPointerCapture(event.pointerId);
			this.#gesture = { kind: 'panning', id: event.pointerId, at };
			return true;
		}
		if (event.isPrimary) {
			this.#touches.clear();
			this.#gesture = { kind: 'idle' };
		}
		this.#touches.set(event.pointerId, { at, start: at, tap: event.isPrimary && hit === 'photo' });
		if (this.#touches.size >= 2) {
			for (const touch of this.#touches.values()) touch.tap = false;
			this.#gesture = this.#pinch();
		} else if (this.zoomed) {
			this.#gesture = { kind: 'panning', id: event.pointerId, at };
		}
		return this.active;
	}

	/** True when the zoom follows the pointer. */
	move(event: PointerEvent): boolean {
		const at = point(event);
		const touch = this.#touches.get(event.pointerId);
		if (touch) {
			touch.at = at;
			if (distance(touch.start, at) > TAP_SLOP_PX) touch.tap = false;
		}
		const gesture = this.#gesture;
		switch (gesture.kind) {
			case 'idle':
				return false;
			case 'panning':
				if (gesture.id !== event.pointerId) return false;
				this.#move(1, gesture.at, at);
				this.#gesture = { ...gesture, at };
				return true;
			case 'pinching': {
				if (!touch) return false;
				const next = this.#pinch();
				this.#move(next.distance / Math.max(1, gesture.distance), gesture.centre, next.centre);
				this.#gesture = next;
				return true;
			}
			default:
				return gesture satisfies never;
		}
	}

	/** A pointer lifted, or was cancelled; true when the zoom was following it. */
	up(event: PointerEvent, cancelled = false): boolean {
		const touch = this.#touches.get(event.pointerId);
		this.#touches.delete(event.pointerId);
		if (touch?.tap && !cancelled) this.#tap(touch.at, event.timeStamp);
		const gesture = this.#gesture;
		switch (gesture.kind) {
			case 'idle':
				return false;
			case 'panning':
				if (gesture.id !== event.pointerId) return false;
				this.#gesture = { kind: 'idle' };
				return true;
			case 'pinching': {
				if (!touch) return false;
				if (this.#touches.size >= 2) {
					this.#gesture = this.#pinch();
					return true;
				}
				if (this.#view.scale < SNAP_SCALE) this.#fit();
				const [rest] = this.#touches;
				this.#gesture =
					rest && this.zoomed ? { kind: 'panning', id: rest[0], at: rest[1].at } : { kind: 'idle' };
				return true;
			}
			default:
				return gesture satisfies never;
		}
	}

	wheel(event: WheelEvent): void {
		event.preventDefault();
		const px = event.deltaY * (event.deltaMode === WheelEvent.DOM_DELTA_PIXEL ? 1 : WHEEL_LINE_PX);
		const zoom = px * (event.ctrlKey ? PINCH_ZOOM_PER_PX : WHEEL_ZOOM_PER_PX);
		const at = point(event);
		this.#move(Math.exp(-Math.min(MAX_WHEEL_ZOOM, Math.max(-MAX_WHEEL_ZOOM, zoom))), at, at);
	}

	#tap(at: Point, t: number) {
		const last = this.#lastTap;
		if (last && t - last.t < DOUBLE_TAP_MS && distance(last, at) < DOUBLE_TAP_SLOP_PX) {
			this.#lastTap = null;
			this.toggle(at);
		} else {
			this.#lastTap = { ...at, t };
		}
	}

	#pinch(): Pinching {
		const [a, b] = [...this.#touches.values()].map((touch) => touch.at);
		return {
			kind: 'pinching',
			distance: distance(a, b),
			centre: { x: (a.x + b.x) / 2, y: (a.y + b.y) / 2 }
		};
	}

	/** Scales by `factor`, moving the point of the photo under `from` to `to`. */
	#move(factor: number, from: Point, to: Point) {
		const area = this.#options.area();
		if (!area) return;
		this.#set(area, this.#view.scale * factor, from, to);
		this.#smooth = false;
	}

	#fit() {
		this.#view = FIT;
		this.#smooth = true;
	}

	#set(area: DOMRect, scale: number, from: Point, to: Point) {
		const view = this.#view;
		const next = Math.min(MAX_SCALE, Math.max(1, scale));
		const k = next / view.scale;
		const c = centre(area);
		const { ratio } = this.#options;
		const width = Math.min(area.width, area.height * ratio);
		const height = width / ratio;
		this.#view = {
			scale: next,
			x: limit(to.x - c.x - k * (from.x - c.x - view.x), (width * next - area.width) / 2),
			y: limit(to.y - c.y - k * (from.y - c.y - view.y), (height * next - area.height) / 2)
		};
		if (view.scale === 1 && next > 1) this.#options.onzoom();
	}
}
