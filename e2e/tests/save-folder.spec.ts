import type { Page } from '@playwright/test';
import { ALBUM_TITLE, ITEMS } from '../fixtures/immich.ts';
import { openGallery } from '../fixtures/page.ts';
import { expect, test } from '../fixtures/test.ts';

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

/** Answers the folder picker with the origin's private folder. */
async function pickPrivateFolder(page: Page) {
	await page.addInitScript(() => {
		window.showDirectoryPicker = () => navigator.storage.getDirectory();
	});
}

/** Leaves the folder picker open until `window.cancelPicker()`. */
async function holdPicker(page: Page) {
	await page.addInitScript(() => {
		window.showDirectoryPicker = () =>
			new Promise((_, reject) => {
				window.cancelPicker = () =>
					reject(new DOMException('The user aborted a request.', 'AbortError'));
			});
	});
}

async function saveAllToFolder(page: Page) {
	await page.getByRole('button', { name: 'Save all' }).click();
	await page.locator('.top-bar').getByRole('button', { name: 'Save to a folder' }).click();
}

const status = (page: Page) => page.locator('.banner').getByRole('status');

for (const hasTouch of [false, true]) {
	test.describe(hasTouch ? 'touch' : 'mouse', () => {
		test.use({ hasTouch });

		test('saves every item into the picked folder', async ({ page }) => {
			await pickPrivateFolder(page);
		await openGallery(page);
		await saveAllToFolder(page);
		await expect(status(page)).toContainText(`Saved to`);
		await expect(status(page)).toContainText(ALBUM_TITLE);
		await expect(status(page)).toContainText('1 item couldn’t be saved.');

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
	});
}

test('saving a selection writes only those files', async ({ page }) => {
	await pickPrivateFolder(page);
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
	await bar.getByRole('button', { name: 'Save', exact: true }).click();
	await bar.getByRole('button', { name: 'Save to a folder' }).click();
	await expect(bar.locator('.count')).toBeHidden();
	await expect(status(page)).toContainText('Saved to');
	await expect(status(page)).not.toContainText('couldn’t be saved');

	const files = await savedFiles(page);
	expect(Object.keys(files).sort()).toEqual(['002.jpg', '004.mp4']);
});

test('closing the folder picker leaves the save idle', async ({ page }) => {
	await holdPicker(page);
	await openGallery(page);
	await saveAllToFolder(page);
	await expect(status(page)).toContainText('Choose a folder to save to');
	await expect(page.getByRole('button', { name: 'Saving…' })).toBeDisabled();

	await page.evaluate(() => window.cancelPicker!());
	await expect(page.locator('.banner')).toHaveCount(0);
	await expect(page.getByRole('button', { name: 'Save all' })).toBeEnabled();
	expect(await savedFiles(page)).toEqual({});
});

test('cancel in the bar stops a running save', async ({ page }) => {
	await holdPicker(page);
	await openGallery(page);
	await saveAllToFolder(page);
	await expect(status(page)).toContainText('Choose a folder to save to');

	await page.locator('.banner').getByRole('button', { name: 'Cancel' }).click();
	await expect(page.locator('.banner')).toHaveCount(0);
	await expect(page.getByRole('button', { name: 'Save all' })).toBeEnabled();
});
