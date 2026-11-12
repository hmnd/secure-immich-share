import { readFile } from 'node:fs/promises';
import type { Page } from '@playwright/test';
import { unzipSync } from 'fflate';
import { ITEMS } from '../fixtures/immich.ts';
import { galleryURL, openGallery, photoURL } from '../fixtures/page.ts';
import { expect, test } from '../fixtures/test.ts';

const tile = (page: Page, index: number) => page.locator(`#p-${ITEMS[index].id}`);
const checkbox = (page: Page, index: number) =>
	page.getByRole('button', { name: `Select photo ${index + 1}` });
const count = (page: Page) => page.locator('.top-bar .count');

test('shift-click selects a range with a preview, then downloads it as a zip', async ({ page }) => {
	await openGallery(page);
	await tile(page, 1).hover();
	await checkbox(page, 1).click();
	await expect(checkbox(page, 1)).toHaveAttribute('aria-pressed', 'true');
	await expect(count(page)).toHaveText('1 selected');

	await page.keyboard.down('Shift');
	await tile(page, 4).hover();
	for (const index of [2, 3, 4]) await expect(tile(page, index)).toHaveClass(/\bcandidate\b/);
	for (const index of [0, 1, 5]) await expect(tile(page, index)).not.toHaveClass(/\bcandidate\b/);
	await tile(page, 4).click();
	await page.keyboard.up('Shift');
	await expect(count(page)).toHaveText('4 selected');
	await expect(page.locator('.tile.candidate')).toHaveCount(0);
	await expect(page.getByRole('dialog')).toBeHidden();

	// While selecting, a click on a tile toggles it.
	await tile(page, 0).click();
	await expect(count(page)).toHaveText('5 selected');
	await tile(page, 0).click();
	await expect(count(page)).toHaveText('4 selected');

	const download = page.waitForEvent('download');
	await page.locator('.top-bar').getByRole('button', { name: 'Download zip' }).click();
	const file = await download;
	expect(file.suggestedFilename()).toBe('photos.zip');
	const entries = Object.keys(unzipSync(await readFile(await file.path()))).sort();
	expect(entries).toEqual(['002.jpg', '003.heic', '004.mp4', '005.heic']);
	await expect(page.locator('.top-bar .banner').getByRole('status')).toHaveText('Download started');
	await expect(count(page)).toBeHidden();
});

test('a single selected item downloads directly', async ({ page }) => {
	await openGallery(page);
	await tile(page, 5).hover();
	await checkbox(page, 5).click();
	const download = page.waitForEvent('download');
	await page.locator('.top-bar').getByRole('button', { name: 'Download', exact: true }).click();
	expect((await download).suggestedFilename()).toBe('006.jpg');
	await expect(page.locator('.top-bar .banner').getByRole('status')).toHaveText('Download started');
});

/** Expects tile `index` to still sit `y` pixels from the top of the screen. */
async function expectTileAt(page: Page, index: number, y: number) {
	await expect
		.poll(async () => Math.abs((await tile(page, index).boundingBox())!.y - y))
		.toBeLessThanOrEqual(1);
}

test('the bar appearing at the top of the page doesn’t move the photos', async ({ page }) => {
	await openGallery(page);
	const y = (await tile(page, 1).boundingBox())!.y;
	await tile(page, 1).hover();
	await checkbox(page, 1).click();
	await expect(count(page)).toHaveText('1 selected');
	await expectTileAt(page, 1, y);
	expect(await page.evaluate(() => window.scrollY)).toBeGreaterThan(0);

	await page.keyboard.press('Escape');
	await expect(count(page)).toBeHidden();
	await expectTileAt(page, 1, y);
});

test('a selection made in the viewer shows its bar once the viewer closes, in place', async ({
	page
}) => {
	await openGallery(page);
	const y = (await tile(page, 1).boundingBox())!.y;
	await page.getByRole('link', { name: ITEMS[1].alt }).click();
	const dialog = page.getByRole('dialog');
	await dialog.getByRole('button', { name: 'Select' }).click();
	await expect(count(page)).toBeHidden();
	await dialog.getByRole('button', { name: 'Close' }).click();
	await expect(count(page)).toHaveText('1 selected');
	await expectTileAt(page, 1, y);
});

test('Escape clears the selection', async ({ page }) => {
	await openGallery(page);
	await tile(page, 2).hover();
	await checkbox(page, 2).click();
	await expect(count(page)).toHaveText('1 selected');
	await page.keyboard.press('Escape');
	await expect(count(page)).toBeHidden();
	await expect(page.locator('.tile.selected')).toHaveCount(0);

	// A click opens the photo again.
	await tile(page, 2).click();
	await expect(page.getByRole('dialog')).toBeVisible();
});

test('going back clears the selection instead of leaving the gallery', async ({ page }) => {
	await openGallery(page);
	await tile(page, 2).hover();
	await checkbox(page, 2).click();
	await expect(count(page)).toHaveText('1 selected');
	await page.goBack();
	await expect(count(page)).toBeHidden();
	await expect(page.locator('.tile.selected')).toHaveCount(0);
	await expect(page).toHaveURL(galleryURL());

	// Clearing it another way drops its history entry.
	await tile(page, 3).hover();
	await checkbox(page, 3).click();
	await expect.poll(() => page.evaluate(() => history.state)).toEqual({ selecting: true });
	await page.locator('.top-bar').getByRole('button', { name: 'Clear selection' }).click();
	await expect.poll(() => page.evaluate(() => history.state)).toBeNull();
	await expect(page).toHaveURL(galleryURL());
});

test('going back after selecting in the viewer closes it, then clears the selection', async ({
	page
}) => {
	await openGallery(page);
	await page.getByRole('link', { name: ITEMS[1].alt }).click();
	const dialog = page.getByRole('dialog');
	await dialog.getByRole('button', { name: 'Select' }).click();
	await page.goBack();
	await expect(dialog).toBeHidden();
	await expect(count(page)).toHaveText('1 selected');
	await expect(page).toHaveURL(galleryURL());

	await page.goBack();
	await expect(count(page)).toBeHidden();
	await expect(page).toHaveURL(galleryURL());
});

test('a selection made on a ?photo= link still clears on back', async ({ page }) => {
	await page.goto(photoURL(ITEMS[2].id));
	const dialog = page.getByRole('dialog');
	await dialog.getByRole('button', { name: 'Select' }).click();
	await dialog.getByRole('button', { name: 'Close' }).click();
	await expect(count(page)).toHaveText('1 selected');

	await page.goBack();
	await expect(count(page)).toBeHidden();
	await expect(page).toHaveURL(galleryURL());
});

test('a click on the hovered tile’s top strip, beside its checkbox, opens the photo', async ({
	page
}) => {
	await openGallery(page);
	await tile(page, 1).hover();
	await expect(checkbox(page, 1)).toBeVisible();
	const box = (await tile(page, 1).boundingBox())!;
	await page.mouse.click(box.x + box.width / 2, box.y + 8);
	await expect(page.getByRole('dialog', { name: ITEMS[1].alt })).toBeVisible();
});

test('the zoom button opens a photo while selecting', async ({ page }) => {
	await openGallery(page);
	await tile(page, 1).hover();
	await checkbox(page, 1).click();
	await tile(page, 3).hover();
	await page.getByRole('button', { name: 'Open photo 4' }).click();
	const dialog = page.getByRole('dialog', { name: ITEMS[3].alt });
	await expect(dialog).toBeVisible();
	await expect(dialog.locator('.counter')).toHaveText('1 selected · 4 / 6');
	await dialog.getByRole('button', { name: 'Close' }).click();
	await expect(count(page)).toHaveText('1 selected');
});

test('selecting from the viewer shows in the bar once it closes', async ({ page }) => {
	await openGallery(page);
	await page.getByRole('link', { name: ITEMS[1].alt }).click();
	const dialog = page.getByRole('dialog');
	const select = dialog.getByRole('button', { name: 'Select' });
	await select.click();
	await expect(select).toHaveAttribute('aria-pressed', 'true');
	await expect(dialog.locator('.counter')).toHaveText('1 selected · 2 / 6');
	await dialog.getByRole('button', { name: 'Next' }).click();
	await expect(select).toHaveAttribute('aria-pressed', 'false');
	await select.click();
	await expect(dialog.locator('.counter')).toHaveText('2 selected · 3 / 6');

	await dialog.getByRole('button', { name: 'Close' }).click();
	await expect(dialog).toBeHidden();
	await expect(count(page)).toHaveText('2 selected');
	await expect(tile(page, 1)).toHaveClass(/\bselected\b/);
	await expect(tile(page, 2)).toHaveClass(/\bselected\b/);
});

test.describe('touch', () => {
	test.use({ hasTouch: true });

	/** Puts a finger on a tile and holds it there; it then moves to other tiles and lifts. */
	async function longPress(page: Page, from: number) {
		const cdp = await page.context().newCDPSession(page);
		const center = async (index: number) => {
			const box = (await tile(page, index).boundingBox())!;
			return { x: box.x + box.width / 2, y: box.y + box.height / 2 };
		};
		const touch = (
			type: 'touchStart' | 'touchMove' | 'touchEnd',
			point: { x: number; y: number } | null
		) => cdp.send('Input.dispatchTouchEvent', { type, touchPoints: point ? [point] : [] });
		await touch('touchStart', await center(from));
		return {
			move: async (index: number) => touch('touchMove', await center(index)),
			end: () => touch('touchEnd', null)
		};
	}

	test('a long press drags out a range; dragging back shrinks it', async ({ page }) => {
		await openGallery(page);
		const drag = await longPress(page, 1);
		await expect(count(page)).toHaveText('1 selected');
		await drag.move(4);
		await expect(count(page)).toHaveText('4 selected');
		await drag.move(2);
		await expect(count(page)).toHaveText('2 selected');
		await drag.end();
		await expect(page.getByRole('dialog')).toBeHidden();
		for (const index of [1, 2]) await expect(tile(page, index)).toHaveClass(/\bselected\b/);
		await expect(page.locator('.tile.selected')).toHaveCount(2);

		// Starting on a selected tile deselects.
		const undo = await longPress(page, 2);
		await expect(count(page)).toHaveText('1 selected');
		await undo.move(1);
		await undo.end();
		await expect(count(page)).toBeHidden();
	});

	test('a tap on a hidden checkbox opens the photo', async ({ page }) => {
		await openGallery(page);
		await expect(checkbox(page, 1)).toHaveCSS('pointer-events', 'none');
		await tile(page, 1).tap({ position: { x: 12, y: 12 } });
		await expect(page.getByRole('dialog', { name: ITEMS[1].alt })).toBeVisible();
		await expect(count(page)).toBeHidden();
	});

	test('a long press at the top of the page keeps the photo under the finger', async ({ page }) => {
		await openGallery(page);
		const y = (await tile(page, 2).boundingBox())!.y;
		const press = await longPress(page, 2);
		await expect(count(page)).toHaveText('1 selected');
		await expectTileAt(page, 2, y);
		await press.end();
	});

	test('while selecting, one tap toggles a checkbox and leaves no hover look', async ({ page }) => {
		await openGallery(page);
		const drag = await longPress(page, 1);
		await expect(count(page)).toHaveText('1 selected');
		await drag.end();

		await checkbox(page, 3).tap();
		await expect(count(page)).toHaveText('2 selected');
		await expect(checkbox(page, 3)).toHaveAttribute('aria-pressed', 'true');
		await checkbox(page, 3).tap();
		await expect(count(page)).toHaveText('1 selected');
		await expect(page.locator('.grid')).toHaveAttribute('data-pointer', 'touch');
		const gradient = await tile(page, 3).evaluate((el) => getComputedStyle(el, '::after').opacity);
		expect(gradient).toBe('0');
		await expect(page.getByRole('button', { name: 'Open photo 4' })).toHaveCSS('opacity', '0');

		await tile(page, 4).tap();
		await expect(count(page)).toHaveText('2 selected');
		await expect(page.getByRole('dialog')).toBeHidden();
	});

	test('one tap on Select right after a swipe selects the new photo', async ({ page }) => {
		await openGallery(page);
		await page.getByRole('link', { name: ITEMS[1].alt }).tap();
		const dialog = page.getByRole('dialog');
		const select = dialog.getByRole('button', { name: 'Select' });
		await select.tap();
		await expect(dialog.locator('.counter')).toHaveText('1 selected · 2 / 6');

		const stage = (await dialog.locator('.stage').boundingBox())!;
		const y = stage.y + stage.height / 2;
		const cdp = await page.context().newCDPSession(page);
		await cdp.send('Input.dispatchTouchEvent', {
			type: 'touchStart',
			touchPoints: [{ x: stage.x + stage.width - 40, y }]
		});
		for (let x = stage.x + stage.width - 60; x > stage.x + 40; x -= 30) {
			await cdp.send('Input.dispatchTouchEvent', { type: 'touchMove', touchPoints: [{ x, y }] });
		}
		await cdp.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] });
		await select.tap();
		await expect(dialog.locator('.counter')).toHaveText('2 selected · 3 / 6');
		await expect(select).toHaveAttribute('aria-pressed', 'true');
		// No hover background lingers on the tapped button.
		await expect(select).toHaveCSS('background-color', 'rgba(0, 0, 0, 0)');
	});

	test('a long press doesn’t open the link menu or drag the tile', async ({ page }) => {
		await openGallery(page);
		const prevented = (pointerType: string) =>
			tile(page, 1)
				.locator('img')
				.evaluate((img, pointerType) => {
					const event = new PointerEvent('contextmenu', {
						pointerType,
						bubbles: true,
						cancelable: true
					});
					img.dispatchEvent(event);
					return event.defaultPrevented;
				}, pointerType);
		expect(await prevented('touch')).toBe(true);
		expect(await prevented('mouse')).toBe(false);
		await expect(tile(page, 1).locator('a')).toHaveAttribute('draggable', 'false');
	});
});
