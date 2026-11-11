import type { ShareItem } from '#lib/api/index.ts';

const dateFormat = new Intl.DateTimeFormat('en-GB', {
	day: 'numeric',
	month: 'long',
	year: 'numeric',
	timeZone: 'UTC'
});

const LOCAL_TIME = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):\d{2}$/;

/** Formats a `YYYY-MM-DDTHH:MM:SS` local time as "1 July 2024"; null if it isn't one. */
export function formatDate(takenAt: string | null): string | null {
	const match = takenAt ? LOCAL_TIME.exec(takenAt) : null;
	if (!match) return null;
	const [, y, m, d] = match;
	return dateFormat.format(Date.UTC(Number(y), Number(m) - 1, Number(d)));
}

/** Formats a `YYYY-MM-DDTHH:MM:SS` local time as "1 July 2024 at 18:00"; null if it isn't one. */
export function formatDateTime(takenAt: string | null): string | null {
	const match = takenAt ? LOCAL_TIME.exec(takenAt) : null;
	if (!match) return null;
	const [, y, m, d, hh, mm] = match;
	return `${dateFormat.format(Date.UTC(Number(y), Number(m) - 1, Number(d)))} at ${hh}:${mm}`;
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
