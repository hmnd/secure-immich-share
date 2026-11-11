// Thin wrappers over the generated SDK (one function per operation id). Call
// sites import these, never the generated client directly.
import './client.ts';
import * as sdk from './generated/sdk.gen.ts';
import type { LinkKind, MediaVariant } from './types.ts';

export { ApiError } from './client.ts';
export { errorMessage, isNotFound } from './errors.ts';
export type * from './types.ts';

const noStore = { cache: 'no-store' } as const;

export const getShareAsset = async (kind: LinkKind, ref: string, id: string) =>
	(await sdk.getShareAsset({ path: { kind, ref, id }, ...noStore })).data;

export const prepareMedia = async (
	kind: LinkKind,
	ref: string,
	id: string,
	variant: MediaVariant,
	signal?: AbortSignal
) => (await sdk.prepareMedia({ path: { kind, ref, id, variant }, signal })).data;

/** Starts or joins the zip of `assetIds`, or of the whole share without them. */
export const startZip = async (kind: LinkKind, ref: string, assetIds?: readonly string[]) =>
	(
		await sdk.startZip({
			path: { kind, ref },
			body: assetIds ? { assetIds: [...assetIds] } : undefined
		})
	).data;

export const getZip = async (kind: LinkKind, ref: string, zipId: string, signal?: AbortSignal) =>
	(await sdk.getZip({ path: { kind, ref, zipId }, signal, ...noStore })).data;
