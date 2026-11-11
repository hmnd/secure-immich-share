import type { Page } from '@playwright/test';
import { ITEMS } from '../fixtures/immich.ts';
import { galleryURL, openGallery, photoURL } from '../fixtures/page.ts';
import { expect, test } from '../fixtures/test.ts';

const historyLength = (page: Page) => page.evaluate(() => history.length);

test('opening a tile pushes history; back closes and forward reopens', async ({ page }) => {
	await openGallery(page);
	const before = await historyLength(page);

	await page.getByRole('link', { name: ITEMS[1].alt }).click();
	const dialog = page.getByRole('dialog', { name: ITEMS[1].alt });
	await expect(dialog).toBeVisible();
	await expect(page).toHaveURL(photoURL(ITEMS[1].id));
	expect(await historyLength(page)).toBe(before + 1);

	await page.goBack();
	await expect(page.getByRole('dialog')).toBeHidden();
	await expect(page).toHaveURL(galleryURL());

	await page.goForward();
	await expect(dialog).toBeVisible();
	await expect(dialog.locator('.counter')).toHaveText('2 / 6');
	await expect(page).toHaveURL(photoURL(ITEMS[1].id));
});

test('arrow keys and buttons move between items and replace the history entry', async ({
	page
}) => {
	await openGallery(page);
	await page.getByRole('link', { name: ITEMS[1].alt }).click();
	const dialog = page.getByRole('dialog');
	const counter = dialog.locator('.counter');
	await expect(dialog).toBeVisible();
	const opened = await historyLength(page);

	await page.keyboard.press('ArrowRight');
	await expect(page).toHaveURL(photoURL(ITEMS[2].id));
	await expect(counter).toHaveText('3 / 6');
	await expect(dialog).toHaveAccessibleName(ITEMS[2].alt);
	await page.keyboard.press('ArrowLeft');
	await expect(page).toHaveURL(photoURL(ITEMS[1].id));

	await dialog.getByRole('button', { name: 'Next' }).click();
	await dialog.getByRole('button', { name: 'Next' }).click();
	await expect(counter).toHaveText('4 / 6');
	await expect(page).toHaveURL(photoURL(ITEMS[3].id));
	await dialog.getByRole('button', { name: 'Previous' }).click();
	await expect(counter).toHaveText('3 / 6');
	await expect(page).toHaveURL(photoURL(ITEMS[2].id));
	expect(await historyLength(page)).toBe(opened);

	await page.goBack();
	await expect(dialog).toBeHidden();
	await expect(page).toHaveURL(galleryURL());
});

test('Escape closes the lightbox and goes back', async ({ page }) => {
	await openGallery(page);
	await page.getByRole('link', { name: ITEMS[4].alt }).click();
	await expect(page.getByRole('dialog')).toBeVisible();
	await page.keyboard.press('Escape');
	await expect(page.getByRole('dialog')).toBeHidden();
	await expect(page).toHaveURL(galleryURL());
});

test('a ?photo= link opens the lightbox directly; closing stays on the page', async ({ page }) => {
	const last = ITEMS[ITEMS.length - 1];
	await page.goto(photoURL(last.id));
	const dialog = page.getByRole('dialog', { name: last.alt });
	await expect(dialog).toBeVisible();
	await expect(dialog.locator('.counter')).toHaveText('6 / 6');
	await expect(dialog.getByRole('button', { name: 'Next' })).toBeHidden();
	const before = await historyLength(page);

	await dialog.getByRole('button', { name: 'Close' }).click();
	await expect(page.getByRole('dialog')).toBeHidden();
	await expect(page).toHaveURL(galleryURL());
	expect(await historyLength(page)).toBe(before);
	await expect(page.locator(`#p-${last.id}`)).toBeInViewport();
});
