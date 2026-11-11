import type { ShareItem } from '#lib/api/index.ts';

const dateFormat = new Intl.DateTimeFormat('en-GB', {
	day: 'numeric',
	month: 'long',
	year: 'numeric',
	timeZone: 'UTC'
});

/** Formats a `YYYY-MM-DD` local date as "1 July 2024"; null if it isn't one. */
export function formatDate(date: string | null): string | null {
	const match = date ? /^(\d{4})-(\d{2})-(\d{2})$/.exec(date) : null;
	if (!match) return null;
	const [, y, m, d] = match;
	return dateFormat.format(Date.UTC(Number(y), Number(m) - 1, Number(d)));
}

function isVideo(item: ShareItem): boolean {
	switch (item.kind) {
		case 'video':
			return true;
		case 'image':
		case 'live':
			return false;
		default:
			return item.kind satisfies never;
	}
}

/** "Photo, 1 July 2024", "Video" and so on. */
export function altText(item: ShareItem): string {
	const noun = isVideo(item) ? 'Video' : 'Photo';
	const date = formatDate(item.takenAt);
	return date ? `${noun}, ${date}` : noun;
}

export const plural = (n: number, one: string, many: string) => `${n} ${n === 1 ? one : many}`;

/** "3 photos", "1 video" or "4 photos & videos". */
export function fileSummary(files: readonly File[]): string {
	const videos = files.filter((file) => file.type.startsWith('video/')).length;
	if (videos === 0) return plural(files.length, 'photo', 'photos');
	if (videos === files.length) return plural(videos, 'video', 'videos');
	return `${files.length} photos & videos`;
}
