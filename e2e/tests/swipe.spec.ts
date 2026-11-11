import type { CDPSession, Page } from '@playwright/test';
import { ITEMS } from '../fixtures/immich.ts';
import { openGallery } from '../fixtures/page.ts';
import { expect, test } from '../fixtures/test.ts';

test.use({ hasTouch: true });

async function openPhoto(page: Page) {
	await openGallery(page);
	await page.getByRole('link', { name: ITEMS[1].alt }).tap();
	const dialog = page.getByRole('dialog', { name: ITEMS[1].alt });
	await expect(dialog).toBeVisible();
	const stage = (await dialog.locator('.stage').boundingBox())!;
	const cdp = await page.context().newCDPSession(page);
	return {
		cdp,
		left: stage.x + 40,
		right: stage.x + stage.width - 40,
		centre: { x: stage.x + stage.width / 2, y: stage.y + stage.height / 2 }
	};
}

const touch = (
	cdp: CDPSession,
	type: 'touchStart' | 'touchMove' | 'touchEnd',
	touchPoints: { x: number; y: number }[]
) => cdp.send('Input.dispatchTouchEvent', { type, touchPoints });

/** Flicks a finger along `y` from `from` to `to`, then lifts it. */
async function flick(cdp: CDPSession, from: number, to: number, y: number) {
	const step = Math.sign(to - from) * 30;
	await touch(cdp, 'touchStart', [{ x: from, y }]);
	for (let x = from + step; Math.abs(to - x) >= 30; x += step) {
		await touch(cdp, 'touchMove', [{ x, y }]);
	}
	await touch(cdp, 'touchEnd', []);
}

async function expectShowing(page: Page, index: number) {
	const { alt } = ITEMS[index];
	const dialog = page.getByRole('dialog', { name: alt });
	await expect(dialog.locator('.counter')).toHaveText(`${index + 1} / ${ITEMS.length}`);
	await expect(dialog.getByRole('img', { name: alt })).toBeInViewport({ ratio: 1 });
}

test('each swipe in quick succession moves on a photo', async ({ page }) => {
	const { cdp, left, right, centre } = await openPhoto(page);

	for (let i = 0; i < 3; i++) await flick(cdp, right, left, centre.y);
	await expectShowing(page, 4);

	for (let i = 0; i < 2; i++) await flick(cdp, left, right, centre.y);
	await expectShowing(page, 2);
});

test('catching a sliding photo and letting go leaves it in place', async ({ page }) => {
	const { cdp, left, right, centre } = await openPhoto(page);
	await flick(cdp, right, left, centre.y);
	await touch(cdp, 'touchStart', [centre]);
	await touch(cdp, 'touchMove', [{ x: centre.x, y: centre.y + 60 }]);
	await touch(cdp, 'touchEnd', []);
	await expectShowing(page, 2);
});
