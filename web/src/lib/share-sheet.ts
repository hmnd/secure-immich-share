import { fetchPrepared, type Prepare } from '#lib/media-loader.svelte.ts';
import { isAndroid, isTouch, mediaType } from '#lib/saving.ts';

/** What one share sheet accepts. Files wait in memory until shared. */
export interface ShareLimits {
	maxFiles: number;
	maxBytes: number;
	/** Accepted MIME types; null accepts every media type. */
	types: ReadonlySet<string> | null;
}

const MiB = 1024 * 1024;

// Chromium checks these in share() but not in canShare(), so they are mirrored here.
const CHROMIUM: ShareLimits = {
	maxFiles: 10,
	maxBytes: 50 * MiB,
	types: new Set(['image/jpeg', 'image/png', 'image/webp', 'image/avif', 'video/mp4'])
};

const OTHER: ShareLimits = { maxFiles: 20, maxBytes: 150 * MiB, types: null };

export function shareLimits(): ShareLimits {
	return 'userAgentData' in navigator ? CHROMIUM : OTHER;
}

/**
 * Whether files can go to the system share sheet ("Save Image" on iOS) on a touch device.
 * Android's share sheet has no way to save to the device, so it gets plain downloads instead.
 */
export function canSaveToPhone(): boolean {
	if (typeof navigator.canShare !== 'function' || !isTouch() || isAndroid()) return false;
	return navigator.canShare({ files: [new File([], 'probe.jpg', { type: 'image/jpeg' })] });
}

/** Whether a share sheet can still open without another tap. */
export function hasActivation(): boolean {
	return navigator.userActivation?.isActive ?? false;
}

/**
 * Reads `res` as a File named `name`; null if the share sheet won't take it.
 * Stops reading the body as soon as the headers rule it out.
 */
async function shareableFile(
	res: Response,
	name: string,
	limits: ShareLimits
): Promise<File | null> {
	const media = mediaType(res.headers.get('content-type'));
	const size = Number(res.headers.get('content-length'));
	if (!media || (limits.types && !limits.types.has(media.type)) || size > limits.maxBytes) {
		return null;
	}
	const blob = await res.blob();
	if (blob.size > limits.maxBytes) return null;
	return new File([blob], `${name}.${media.ext}`, { type: media.type });
}

export type Loaded =
	| { kind: 'file'; file: File; url: string }
	/** Ready, but only as a plain download. */
	| { kind: 'download'; url: string }
	| { kind: 'unavailable' };

/**
 * Prepares each variant in turn until one gives a file the share sheet takes.
 * Throws on failures worth retrying, such as a dropped connection, and once `signal` aborts.
 */
export async function loadShareable(
	variants: readonly Prepare[],
	name: string,
	limits: ShareLimits,
	signal: AbortSignal
): Promise<Loaded> {
	const fetched = await fetchPrepared(variants, signal, (res) => shareableFile(res, name, limits));
	switch (fetched.kind) {
		case 'taken':
			return { kind: 'file', file: fetched.value, url: fetched.url };
		case 'refused':
			return { kind: 'download', url: fetched.url };
		case 'unavailable':
			return fetched;
		default:
			return fetched satisfies never;
	}
}

/** Opens the share sheet. Must run synchronously inside a tap handler. */
export async function shareFiles(files: File[]): Promise<'shared' | 'dismissed' | 'failed'> {
	try {
		await navigator.share({ files });
		return 'shared';
	} catch (err) {
		// AbortError: the sheet was closed; NotAllowedError: the tap expired.
		if (
			err instanceof DOMException &&
			(err.name === 'AbortError' || err.name === 'NotAllowedError')
		) {
			return 'dismissed';
		}
		return 'failed';
	}
}
