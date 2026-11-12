import type { Download, Page } from "@playwright/test";
import { ITEMS } from "../fixtures/immich.ts";
import { openGallery } from "../fixtures/page.ts";
import { expect, test } from "../fixtures/test.ts";

// Android browsers without a folder picker save each item as its own download.
test.use({
  hasTouch: true,
  userAgent:
    "Mozilla/5.0 (Android 15; Mobile; rv:146.0) Gecko/146.0 Firefox/146.0",
});

test.beforeEach(async ({ page }) => {
  await page.addInitScript(() => {
    window.showDirectoryPicker = undefined;
  });
});

function collectDownloads(page: Page): Download[] {
  const downloads: Download[] = [];
  page.on("download", (download) => downloads.push(download));
  return downloads;
}

test("save all downloads every item on its own", async ({ page }) => {
  const downloads = collectDownloads(page);
  await openGallery(page);
  await page.getByRole("button", { name: "Save all" }).click();
  const bar = page.locator(".top-bar");
  await expect(
    bar.getByRole("button", { name: "Download as zip" }),
  ).toBeVisible();
  await bar.getByRole("button", { name: "Download one by one" }).click();
  const status = page.locator(".banner").getByRole("status");
  await expect(status).toContainText("Saved to Downloads");
  await expect(status).toContainText("1 item couldn’t be downloaded.");

  await expect
    .poll(() => downloads.map((d) => d.suggestedFilename()))
    .toEqual(["002.jpg", "003.heic", "004.mp4", "005.heic", "006.jpg"]);
});

test("saving a selection downloads only those items", async ({ page }) => {
  const downloads = collectDownloads(page);
  await openGallery(page);
  await page.getByRole("link", { name: ITEMS[1].alt }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByRole("button", { name: "Select" }).click();
  await dialog.getByRole("button", { name: "Next" }).click();
  await dialog.getByRole("button", { name: "Next" }).click();
  await dialog.getByRole("button", { name: "Select" }).click();
  await dialog.getByRole("button", { name: "Close" }).click();

  const bar = page.locator(".top-bar");
  await bar.getByRole("button", { name: "Save", exact: true }).click();
  await expect(
    bar.getByRole("button", { name: "Download as zip" }),
  ).toBeVisible();
  await bar.getByRole("button", { name: "Download one by one" }).click();
  const status = bar.locator(".banner").getByRole("status");
  await expect(status).toContainText("Saved to Downloads");
  await expect(status).not.toContainText("couldn’t be downloaded");

  await expect
    .poll(() => downloads.map((d) => d.suggestedFilename()))
    .toEqual(["002.jpg", "004.mp4"]);
});
