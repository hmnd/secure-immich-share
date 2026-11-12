import { readFile } from 'node:fs/promises';
import type { Download, Page } from '@playwright/test';
import { unzipSync } from 'fflate';
import { SAVEABLE } from '../fixtures/immich.ts';
import { openGallery } from '../fixtures/page.ts';
import { expect, test } from '../fixtures/test.ts';

async function expectShareZip(page: Page, file: Download) {
	expect(file.suggestedFilename()).toBe('photos.zip');
	const entries = Object.keys(unzipSync(await readFile(await file.path()))).sort();
	expect(entries.filter((name) => /^\d{3}\.\w+$/.test(name))).toHaveLength(SAVEABLE);
	expect(entries).toEqual(expect.arrayContaining(['MISSING.txt']));
	expect(entries).toHaveLength(SAVEABLE + 1);
	const status = page.locator('.top-bar .banner').getByRole('status');
	await expect(status).toContainText('Download started');
	await expect(status).toContainText('1 item couldn’t be included.');
}

test('save all offers the zip, then prepares and downloads it', async ({ page }) => {
	await openGallery(page);
	await page.getByRole('button', { name: 'Save all' }).click();
	const bar = page.locator('.top-bar');
	await expect(bar.getByText('Save 6 items')).toBeVisible();
	await expect(bar.getByRole('button', { name: 'Save to a folder' })).toBeVisible();
	const download = page.waitForEvent('download');
	await bar.getByRole('button', { name: 'Download as zip' }).click();
	await expect(bar.getByText('Save 6 items')).toBeHidden();
	await expectShareZip(page, await download);
});

test.describe('without a folder picker', () => {
	test.beforeEach(async ({ page }) => {
		await page.addInitScript(() => {
			window.showDirectoryPicker = undefined;
		});
	});

	test('download all starts the zip straight away', async ({ page }) => {
		await openGallery(page);
		const download = page.waitForEvent('download');
		await page.getByRole('button', { name: 'Download all' }).click();
		await expectShareZip(page, await download);
	});
});
