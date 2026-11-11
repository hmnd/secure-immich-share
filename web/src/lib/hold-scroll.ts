import type { Attachment } from 'svelte/attachments';

/**
 * Keeps the page below an element at the top of the document still while the element grows or
 * shrinks, by scrolling the same amount. Browsers only do this themselves (scroll anchoring)
 * once the page is scrolled, and Safari not at all, so a bar appearing at the top would
 * otherwise push the page down under the visitor's finger.
 */
export const holdScroll: Attachment<HTMLElement> = (node) => {
	let height = node.offsetHeight;
	/** The scroll position before the latest change, so scroll anchoring can be told apart. */
	let scrolled = window.scrollY;
	const onscroll = () => {
		scrolled = window.scrollY;
	};
	const observer = new ResizeObserver(() => {
		const grown = node.offsetHeight - height;
		height = node.offsetHeight;
		// Room to scroll by the element's height, even on a page shorter than the screen.
		document.body.style.minHeight = height > 0 ? `calc(100dvh + ${height}px)` : '';
		const missing = grown - (window.scrollY - scrolled);
		if (missing !== 0) window.scrollBy({ top: missing, behavior: 'instant' });
		scrolled = window.scrollY;
	});
	window.addEventListener('scroll', onscroll, { passive: true });
	observer.observe(node);
	return () => {
		observer.disconnect();
		document.body.style.minHeight = '';
		window.removeEventListener('scroll', onscroll);
	};
};
