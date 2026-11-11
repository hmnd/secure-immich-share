import { readFile } from 'node:fs/promises';
import { unzipSync } from 'fflate';
import { SAVEABLE } from '../fixtures/immich.ts';
import { openGallery } from '../fixtures/page.ts';
import { expect, test } from '../fixtures/test.ts';

test('download all prepares the zip, then downloads it', async ({ page }) => {
	await openGallery(page);
	const download = page.waitForEvent('download');
	await page.getByRole('button', { name: 'Download all' }).click();
	const file = await download;
	expect(file.suggestedFilename()).toBe('photos.zip');
	const entries = Object.keys(unzipSync(await readFile(await file.path()))).sort();
	expect(entries.filter((name) => /^\d{3}\.\w+$/.test(name))).toHaveLength(SAVEABLE);
	expect(entries).toEqual(expect.arrayContaining(['MISSING.txt']));
	expect(entries).toHaveLength(SAVEABLE + 1);
	await expect(page.getByRole('status')).toHaveText('1 item skipped');
});
