/** Starts a browser download of a same-origin URL. */
export function download(url: string): void {
	const a = document.createElement('a');
	a.href = url;
	a.download = '';
	a.hidden = true;
	document.body.append(a);
	a.click();
	a.remove();
}
