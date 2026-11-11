// Mirrors server/tests/fakes/immich/seed.go.

/** Share keys: base64url of 50 bytes of `n`. */
const key = (n: number) => Buffer.alloc(50, n).toString('base64url');

export const ALBUM_TITLE = 'Summer 2024';
export const PUBLIC_PATH = `/share/${key(1)}`;

const id = (n: number) => `a0000000-0000-4000-8000-00000000000${n}`;

/** The album's items as the gallery lists them (newest first); trashed and locked ones are left out. */
export const ITEMS = [
	{ id: id(7), alt: 'Photo, 6 July 2024' },
	{ id: id(6), alt: 'Photo, 5 July 2024' },
	{ id: id(4), alt: 'Photo, 4 July 2024' },
	{ id: id(3), alt: 'Video, 3 July 2024' },
	{ id: id(2), alt: 'Photo, 2 July 2024' },
	{ id: id(1), alt: 'Photo, 1 July 2024' }
] as const;

/** Every source of this item fails verification. */
export const BROKEN_ID = ITEMS[0].id;
/** Items that can be saved or zipped: all but the broken one. */
export const SAVEABLE = ITEMS.length - 1;
