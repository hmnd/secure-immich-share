import type { LinkKind, ShareView } from '#lib/api/index.ts';
import { canSaveToFolder, saveVariants } from '#lib/saving.ts';
import { canSaveToPhone } from '#lib/share-sheet.ts';
import { SaveAllJob } from '#lib/save-all.svelte.ts';
import { folderName, SaveFolderJob } from '#lib/save-folder.svelte.ts';

export type SaveJob = SaveFolderJob | SaveAllJob;

/** How this device saves a whole share to the phone: a picked folder, share sheet batches, or not at all. */
export function createSaveJob(kind: LinkKind, ref: string, share: ShareView): SaveJob | null {
	if (!share.items.some((item) => saveVariants(item).length > 0)) return null;
	if (canSaveToFolder()) return new SaveFolderJob(kind, ref, share.items, folderName(share.title));
	if (canSaveToPhone()) return new SaveAllJob(kind, ref, share.items);
	return null;
}
