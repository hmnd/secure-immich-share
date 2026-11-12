import { thumbHashToDataURL } from '#lib/thumbhash.ts';

interface Row {
	height: number;
	tiles: { index: number; width: number }[];
	/** Whether the row spans the full width; the last row may stop short. */
	full: boolean;
}

const RETRY_MS = 2000;
/** How far past the viewport thumbnails load: half a screen above and below. */
const NEAR_VIEWPORT = '50% 0px';

/** Justified rows: each row grows until it fits `width` at no more than `target` height. */
function layoutRows(ratios: readonly number[], width: number, gap: number, target: number): Row[] {
	const rows: Row[] = [];
	let pending: { index: number; ratio: number }[] = [];
	let sum = 0;
	const finish = (height: number, full: boolean) => {
		rows.push({
			height,
			tiles: pending.map(({ index, ratio }) => ({ index, width: ratio * height })),
			full
		});
		pending = [];
		sum = 0;
	};
	ratios.forEach((ratio, index) => {
		pending.push({ index, ratio });
		sum += ratio;
		const height = (width - gap * (pending.length - 1)) / sum;
		if (height <= target) finish(height, true);
	});
	if (pending.length > 0) finish(target, false);
	return rows;
}

/**
 * Loads thumbnails only near the viewport. The page's loading="lazy" (all there is without
 * JavaScript) starts thumbnails far outside it on slow connections, where they take bandwidth
 * from the visible ones. A thumbnail that leaves this range before it has loaded gives up its
 * src, cancelling the request, and gets it back when it returns.
 */
function loadNearViewport(imgs: readonly HTMLImageElement[]): void {
	const observer = new IntersectionObserver(
		(entries) => {
			for (const { target, isIntersecting } of entries) {
				const img = target as HTMLImageElement;
				const held = img.dataset.src;
				if (isIntersecting && held !== undefined) {
					delete img.dataset.src;
					img.setAttribute('src', held);
				} else if (!isIntersecting && held === undefined && !img.complete) {
					img.dataset.src = img.getAttribute('src') ?? '';
					img.removeAttribute('src');
					img.style.visibility = 'hidden';
				}
			}
		},
		{ rootMargin: NEAR_VIEWPORT }
	);
	for (const img of imgs) observer.observe(img);
}

/**
 * Enhances the server-rendered grid: thumbhash placeholders, thumbnails loaded near the
 * viewport first, one retry for thumbnails that failed (e.g. a busy server), and justified
 * rows of the measured width in place of the CSS layout. Rows aim for the grid's --row-height.
 */
export function enhanceGrid(grid: HTMLElement): void {
	const tiles = [...grid.querySelectorAll<HTMLElement>('.tile')];
	const ratios = tiles.map((tile) => parseFloat(tile.style.getPropertyValue('--r')) || 1);
	const imgs = tiles.flatMap((tile) => tile.querySelector('img') ?? []);

	const failed = (img: HTMLImageElement) => {
		img.style.visibility = 'hidden';
		if (img.dataset.retried) return;
		img.dataset.retried = 'true';
		// Setting src, even to the same value, loads the image again. A held one loads when it
		// comes back near the viewport instead.
		setTimeout(() => {
			const src = img.getAttribute('src');
			if (src !== null) img.setAttribute('src', src);
		}, RETRY_MS);
	};

	for (const tile of tiles) {
		const placeholder = tile.dataset.thumbhash ? thumbHashToDataURL(tile.dataset.thumbhash) : null;
		if (placeholder) tile.style.backgroundImage = `url("${placeholder}")`;
	}
	for (const img of imgs) {
		if (img.complete && img.naturalWidth === 0) failed(img);
	}
	loadNearViewport(imgs);
	grid.addEventListener(
		'load',
		(event) => {
			if (event.target instanceof HTMLImageElement) event.target.style.visibility = '';
		},
		true
	);
	grid.addEventListener(
		'error',
		(event) => {
			if (event.target instanceof HTMLImageElement && event.target.hasAttribute('src')) {
				failed(event.target);
			}
		},
		true
	);

	let width = 0;
	let viewport = 0;
	const observer = new ResizeObserver(([entry]) => {
		const next = entry.contentRect.width;
		if (next === 0 || next === width) return;
		// The grid widened without the window changing, so the scrollbar went away. Laying out
		// the wider rows could make the page tall enough to need it again, and so on forever;
		// the full rows stretch to the new width instead.
		if (next > width && window.innerWidth === viewport) return;
		width = next;
		viewport = window.innerWidth;
		const style = getComputedStyle(grid);
		const gap = parseFloat(style.rowGap) || 0;
		const target = parseFloat(style.getPropertyValue('--row-height'));
		const rows = layoutRows(ratios, width, gap, target).map((row) => {
			const el = document.createElement('div');
			el.className = row.full ? 'row full' : 'row';
			el.style.height = `${row.height}px`;
			for (const { index, width } of row.tiles) {
				tiles[index].style.flexBasis = `${width}px`;
				el.append(tiles[index]);
			}
			return el;
		});
		grid.replaceChildren(...rows);
		grid.classList.add('measured');
	});
	observer.observe(grid);
}
