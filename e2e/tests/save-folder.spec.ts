import type { Page } from '@playwright/test';
import { ALBUM_TITLE, ITEMS } from '../fixtures/immich.ts';
import { openGallery } from '../fixtures/page.ts';
import { expect, test } from '../fixtures/test.ts';

// Saving to a folder is offered on touch devices only.
test.use({ hasTouch: true });

/** The names and sizes of the files saved in the share's folder. */
function savedFiles(page: Page) {
	return page.evaluate(async (folder) => {
		const root = await navigator.storage.getDirectory();
		const dir = await root.getDirectoryHandle(folder).catch(() => null);
		const files: Record<string, number> = {};
		if (!dir) return files;
		for await (const handle of dir.values()) {
			if (handle.kind === 'file') {
				files[handle.name] = (await (handle as FileSystemFileHandle).getFile()).size;
			}
		}
		return files;
	}, ALBUM_TITLE);
}

test('saves every item into the picked folder', async ({ page }) => {
	await page.addInitScript(() => {
		window.showDirectoryPicker = () => navigator.storage.getDirectory();
	});
	await openGallery(page);
	await page.getByRole('button', { name: 'Save all' }).click();
	const status = page.locator('.banner').getByRole('status');
	await expect(status).toContainText(`Saved to`);
	await expect(status).toContainText(ALBUM_TITLE);
	await expect(status).toContainText('1 item couldn’t be saved.');

	const files = await savedFiles(page);
	expect(Object.keys(files).sort()).toEqual([
		'002.jpg',
		'003.heic',
		'004.mp4',
		'005.heic',
		'006.jpg'
	]);
	for (const size of Object.values(files)) expect(size).toBeGreaterThan(0);
});

test('saving a selection writes only those files', async ({ page }) => {
	await page.addInitScript(() => {
		window.showDirectoryPicker = () => navigator.storage.getDirectory();
	});
	await openGallery(page);
	await page.getByRole('link', { name: ITEMS[1].alt }).click();
	const dialog = page.getByRole('dialog');
	await dialog.getByRole('button', { name: 'Select' }).click();
	await dialog.getByRole('button', { name: 'Next' }).click();
	await dialog.getByRole('button', { name: 'Next' }).click();
	await dialog.getByRole('button', { name: 'Select' }).click();
	await dialog.getByRole('button', { name: 'Close' }).click();

	const bar = page.locator('.top-bar');
	await expect(bar.locator('.count')).toHaveText('2 selected');
	await bar.getByRole('button', { name: 'Save to phone' }).click();
	await expect(bar.locator('.count')).toBeHidden();
	const status = bar.locator('.banner').getByRole('status');
	await expect(status).toContainText('Saved to');
	await expect(status).not.toContainText('couldn’t be saved');

	const files = await savedFiles(page);
	expect(Object.keys(files).sort()).toEqual(['002.jpg', '004.mp4']);
});

test('closing the folder picker leaves the save idle', async ({ page }) => {
	await page.addInitScript(() => {
		window.showDirectoryPicker = () =>
			new Promise((_, reject) => {
				window.cancelPicker = () =>
					reject(new DOMException('The user aborted a request.', 'AbortError'));
			});
	});
	await openGallery(page);
	await page.getByRole('button', { name: 'Save all' }).click();
	await expect(page.locator('.banner').getByRole('status')).toContainText(
		'Choose a folder to save to'
	);
	await expect(page.getByRole('button', { name: 'Saving…' })).toBeDisabled();

	await page.evaluate(() => window.cancelPicker!());
	await expect(page.locator('.banner')).toHaveCount(0);
	await expect(page.getByRole('button', { name: 'Save all' })).toBeEnabled();
	expect(await savedFiles(page)).toEqual({});
});
