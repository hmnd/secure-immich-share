import { thumbHashToDataURL } from '#lib/thumbhash.ts';

interface Row {
	height: number;
	tiles: { index: number; width: number }[];
}

const RETRY_MS = 2000;

/** Justified rows: each row grows until it fits `width` at no more than `target` height. */
function layoutRows(ratios: readonly number[], width: number, gap: number, target: number): Row[] {
	const rows: Row[] = [];
	let pending: { index: number; ratio: number }[] = [];
	let sum = 0;
	const finish = (height: number) => {
		rows.push({
			height,
			tiles: pending.map(({ index, ratio }) => ({ index, width: ratio * height }))
		});
		pending = [];
		sum = 0;
	};
	ratios.forEach((ratio, index) => {
		pending.push({ index, ratio });
		sum += ratio;
		const height = (width - gap * (pending.length - 1)) / sum;
		if (height <= target) finish(height);
	});
	if (pending.length > 0) finish(target);
	return rows;
}

/**
 * Enhances the server-rendered grid: thumbhash placeholders, one retry for thumbnails that
 * failed (e.g. a busy server), and justified rows of the measured width in place of the CSS
 * layout. Rows aim for the grid's --row-height.
 */
export function enhanceGrid(grid: HTMLElement): void {
	const tiles = [...grid.querySelectorAll<HTMLElement>('.tile')];
	const ratios = tiles.map((tile) => parseFloat(tile.style.getPropertyValue('--r')) || 1);

	const failed = (img: HTMLImageElement) => {
		img.style.visibility = 'hidden';
		if (img.dataset.retried) return;
		img.dataset.retried = 'true';
		// Setting src, even to the same value, loads the image again.
		setTimeout(() => img.setAttribute('src', img.getAttribute('src') ?? ''), RETRY_MS);
	};

	for (const tile of tiles) {
		const placeholder = tile.dataset.thumbhash ? thumbHashToDataURL(tile.dataset.thumbhash) : null;
		if (placeholder) tile.style.backgroundImage = `url("${placeholder}")`;
		const img = tile.querySelector('img');
		if (img?.complete && img.naturalWidth === 0) failed(img);
	}
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
			if (event.target instanceof HTMLImageElement) failed(event.target);
		},
		true
	);

	let width = 0;
	const observer = new ResizeObserver(([entry]) => {
		const next = entry.contentRect.width;
		if (next === 0 || next === width) return;
		width = next;
		const style = getComputedStyle(grid);
		const gap = parseFloat(style.rowGap) || 0;
		const target = parseFloat(style.getPropertyValue('--row-height'));
		const rows = layoutRows(ratios, width, gap, target).map((row) => {
			const el = document.createElement('div');
			el.className = 'row';
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
