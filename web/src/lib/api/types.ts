// API types, generated from api/openapi.json (`just gen-api`); never
// hand-write them.
import type { GetShareAssetData, PrepareMediaData } from './generated/types.gen.ts';

export type { ShareItem, ShareView, ZipStatus } from './generated/types.gen.ts';

export type LinkKind = GetShareAssetData['path']['kind'];

export type MediaVariant = PrepareMediaData['path']['variant'];
