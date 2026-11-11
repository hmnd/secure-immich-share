import type { CDPSession, Locator, Page } from '@playwright/test';
import { ITEMS } from '../fixtures/immich.ts';
import { openGallery } from '../fixtures/page.ts';
import { expect, test } from '../fixtures/test.ts';

async function openPhoto(page: Page) {
	await openGallery(page);
	await page.getByRole('link', { name: ITEMS[1].alt }).click();
	const dialog = page.getByRole('dialog');
	await expect(dialog).toBeVisible();
	const frame = dialog.locator('.slide:not([inert]) .frame');
	const box = (await frame.boundingBox())!;
	return { dialog, frame, x: box.x + box.width / 2, y: box.y + box.height / 2 };
}

const scale = (frame: Locator) => frame.evaluate((el) => Number((el as HTMLElement).style.scale));
const offset = (frame: Locator) => frame.evaluate((el) => (el as HTMLElement).style.translate);

test('zoom in on the full-size photo, drag around and zoom out', async ({ page }) => {
	const { dialog, frame, x, y } = await openPhoto(page);
	await dialog.getByRole('button', { name: 'Zoom in' }).click();
	await expect(frame.locator('img')).toHaveAttribute('src', /\/full$/);

	const before = await offset(frame);
	await page.mouse.move(x, y);
	await page.mouse.down();
	await page.mouse.move(x + 150, y + 150, { steps: 5 });
	await page.mouse.up();
	expect(await offset(frame)).not.toBe(before);

	await dialog.getByRole('button', { name: 'Zoom out' }).click();
	await expect.poll(() => scale(frame)).toBe(1);
});

test.describe('touch', () => {
	test.use({ hasTouch: true });

	const touch = (
		cdp: CDPSession,
		type: 'touchStart' | 'touchMove' | 'touchEnd',
		touchPoints: { x: number; y: number }[]
	) => cdp.send('Input.dispatchTouchEvent', { type, touchPoints });

	test('pinch to zoom, pan with a finger and double tap to fit', async ({ page }) => {
		const { dialog, frame, x, y } = await openPhoto(page);
		const cdp = await page.context().newCDPSession(page);

		await touch(cdp, 'touchStart', [{ x: x - 20, y }]);
		await touch(cdp, 'touchStart', [
			{ x: x - 20, y },
			{ x: x + 20, y }
		]);
		for (let d = 40; d <= 160; d += 20) {
			await touch(cdp, 'touchMove', [
				{ x: x - d, y },
				{ x: x + d, y }
			]);
		}
		await touch(cdp, 'touchEnd', []);
		await expect.poll(() => scale(frame)).toBeGreaterThan(1);

		const before = await offset(frame);
		await touch(cdp, 'touchStart', [{ x, y }]);
		for (let dx = 30; dx <= 300; dx += 30) {
			await touch(cdp, 'touchMove', [{ x: x - dx, y }]);
		}
		await touch(cdp, 'touchEnd', []);
		expect(await offset(frame)).not.toBe(before);
		await expect(dialog.locator('.counter')).toHaveText('2 / 6');

		for (let i = 0; i < 2; i++) {
			await touch(cdp, 'touchStart', [{ x, y }]);
			await touch(cdp, 'touchEnd', []);
		}
		await expect.poll(() => scale(frame)).toBe(1);
	});
});
