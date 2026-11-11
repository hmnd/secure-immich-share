import type { ShareItem } from '#lib/api/index.ts';

/** How many items a save prepares and fetches at once. */
export const CONCURRENCY = 3;

const EXTENSIONS: Record<string, string> = {
	'image/jpeg': 'jpg',
	'image/png': 'png',
	'image/webp': 'webp',
	'image/avif': 'avif',
	'image/heic': 'heic',
	'video/mp4': 'mp4',
	'video/quicktime': 'mov'
};

export const isTouch = () => matchMedia('(pointer: coarse)').matches;

export const isAndroid = () => /Android/i.test(navigator.userAgent);

/** Whether a touch device can write straight into a folder the visitor picks (Chrome on Android). */
export function canSaveToFolder(): boolean {
	return typeof window.showDirectoryPicker === 'function' && isTouch();
}

/** The MIME type and file extension for a Content-Type header; null for types that aren't media. */
export function mediaType(contentType: string | null): { type: string; ext: string } | null {
	const type = contentType?.split(';')[0].trim() ?? '';
	const ext = EXTENSIONS[type];
	return ext ? { type, ext } : null;
}

/** "001", matching the server's download names. */
export const fileName = (index: number) => String(index + 1).padStart(3, '0');

/** The variants worth saving, best first. */
export function saveVariants(item: ShareItem): ('download' | 'full')[] {
	return (['download', 'full'] as const).filter((v) => item.variants.includes(v));
}

/** Runs `tasks` at most `limit` at a time and yields their results in order. */
export async function* inOrder<T>(tasks: (() => Promise<T>)[], limit: number): AsyncGenerator<T> {
	const running: Promise<T>[] = [];
	let next = 0;
	while (next < tasks.length && running.length < limit) running.push(tasks[next++]());
	while (running.length > 0) {
		const result = await running.shift()!;
		if (next < tasks.length) running.push(tasks[next++]());
		yield result;
	}
}
