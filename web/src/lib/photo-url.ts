/** The gallery URL that opens photo `id` in the viewer. */
export function photoURL(id: string): string {
	return `${location.pathname}?photo=${encodeURIComponent(id)}`;
}

/** The id of the photo the current URL opens, or null for the gallery. */
export function photoParam(): string | null {
	return new URLSearchParams(location.search).get('photo');
}
