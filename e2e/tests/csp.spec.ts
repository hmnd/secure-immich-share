import { ITEMS } from '../fixtures/immich.ts';
import { openGallery } from '../fixtures/page.ts';
import { CSP_VIOLATION, expect, test } from '../fixtures/test.ts';

test('the islands run without tripping the CSP', async ({ page }) => {
	const violations: string[] = [];
	page.on('console', (msg) => {
		if (msg.text().startsWith(CSP_VIOLATION)) violations.push(msg.text());
	});
	await openGallery(page);
	// The lightbox parses the item's details with zod.
	const detail = page.waitForResponse((res) => res.url().endsWith(`/assets/${ITEMS[1].id}`));
	await page.getByRole('link', { name: ITEMS[1].alt }).click();
	await detail;
	// Console events arrive before the answer to a later round trip.
	await page.evaluate(() => new Promise(requestAnimationFrame));
	expect(violations).toEqual([]);
});
