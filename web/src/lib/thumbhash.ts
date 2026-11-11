/*
 * ThumbHash decoder, ported from https://github.com/evanw/thumbhash.
 *
 * Copyright (c) 2023 Evan Wallace
 *
 * Permission is hereby granted, free of charge, to any person obtaining a copy
 * of this software and associated documentation files (the "Software"), to deal
 * in the Software without restriction, including without limitation the rights
 * to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
 * copies of the Software, and to permit persons to whom the Software is
 * furnished to do so, subject to the following conditions:
 *
 * The above copyright notice and this permission notice shall be included in all
 * copies or substantial portions of the Software.
 *
 * THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
 * IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
 * FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
 * AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
 * LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
 * OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
 * SOFTWARE.
 */

function approximateAspectRatio(hash: Uint8Array): number {
	const header = hash[3];
	const hasAlpha = hash[2] & 0x80;
	const isLandscape = hash[4] & 0x80;
	const lx = isLandscape ? (hasAlpha ? 5 : 7) : header & 7;
	const ly = isLandscape ? header & 7 : hasAlpha ? 5 : 7;
	return lx / ly;
}

function thumbHashToRGBA(hash: Uint8Array): { w: number; h: number; rgba: Uint8Array } {
	const { PI, min, max, cos, round } = Math;

	const header24 = hash[0] | (hash[1] << 8) | (hash[2] << 16);
	const header16 = hash[3] | (hash[4] << 8);
	const lDc = (header24 & 63) / 63;
	const pDc = ((header24 >> 6) & 63) / 31.5 - 1;
	const qDc = ((header24 >> 12) & 63) / 31.5 - 1;
	const lScale = ((header24 >> 18) & 31) / 31;
	const hasAlpha = header24 >> 23;
	const pScale = ((header16 >> 3) & 63) / 63;
	const qScale = ((header16 >> 9) & 63) / 63;
	const isLandscape = header16 >> 15;
	const lx = max(3, isLandscape ? (hasAlpha ? 5 : 7) : header16 & 7);
	const ly = max(3, isLandscape ? header16 & 7 : hasAlpha ? 5 : 7);
	const aDc = hasAlpha ? (hash[5] & 15) / 15 : 1;
	const aScale = (hash[5] >> 4) / 15;

	const acStart = hasAlpha ? 6 : 5;
	let acIndex = 0;
	const decodeChannel = (nx: number, ny: number, scale: number) => {
		const ac: number[] = [];
		for (let cy = 0; cy < ny; cy++)
			for (let cx = cy ? 0 : 1; cx * ny < nx * (ny - cy); cx++)
				ac.push(
					(((hash[acStart + (acIndex >> 1)] >> ((acIndex++ & 1) << 2)) & 15) / 7.5 - 1) * scale
				);
		return ac;
	};
	const lAc = decodeChannel(lx, ly, lScale);
	// Saturation is boosted by 1.25x to compensate for quantization.
	const pAc = decodeChannel(3, 3, pScale * 1.25);
	const qAc = decodeChannel(3, 3, qScale * 1.25);
	const aAc = hasAlpha ? decodeChannel(5, 5, aScale) : [];

	const ratio = approximateAspectRatio(hash);
	const w = round(ratio > 1 ? 32 : 32 * ratio);
	const h = round(ratio > 1 ? 32 / ratio : 32);
	const rgba = new Uint8Array(w * h * 4);
	const fx: number[] = [];
	const fy: number[] = [];
	for (let y = 0, i = 0; y < h; y++) {
		for (let x = 0; x < w; x++, i += 4) {
			let l = lDc;
			let p = pDc;
			let q = qDc;
			let a = aDc;

			for (let cx = 0, n = max(lx, hasAlpha ? 5 : 3); cx < n; cx++)
				fx[cx] = cos((PI / w) * (x + 0.5) * cx);
			for (let cy = 0, n = max(ly, hasAlpha ? 5 : 3); cy < n; cy++)
				fy[cy] = cos((PI / h) * (y + 0.5) * cy);

			for (let cy = 0, j = 0; cy < ly; cy++)
				for (let cx = cy ? 0 : 1, fy2 = fy[cy] * 2; cx * ly < lx * (ly - cy); cx++, j++)
					l += lAc[j] * fx[cx] * fy2;

			for (let cy = 0, j = 0; cy < 3; cy++) {
				for (let cx = cy ? 0 : 1, fy2 = fy[cy] * 2; cx < 3 - cy; cx++, j++) {
					const f = fx[cx] * fy2;
					p += pAc[j] * f;
					q += qAc[j] * f;
				}
			}

			if (hasAlpha)
				for (let cy = 0, j = 0; cy < 5; cy++)
					for (let cx = cy ? 0 : 1, fy2 = fy[cy] * 2; cx < 5 - cy; cx++, j++)
						a += aAc[j] * fx[cx] * fy2;

			const b = l - (2 / 3) * p;
			const r = (3 * l - b + q) / 2;
			const g = r - q;
			rgba[i] = max(0, 255 * min(1, r));
			rgba[i + 1] = max(0, 255 * min(1, g));
			rgba[i + 2] = max(0, 255 * min(1, b));
			rgba[i + 3] = max(0, 255 * min(1, a));
		}
	}
	return { w, h, rgba };
}

const CRC_TABLE = [
	0, 498536548, 997073096, 651767980, 1994146192, 1802195444, 1303535960, 1342533948, -306674912,
	-267414716, -690576408, -882789492, -1687895376, -2032938284, -1609899400, -1111625188
];

/** Encodes RGBA pixels (at most 100x100) as an uncompressed PNG data URL. */
function rgbaToDataURL(w: number, h: number, rgba: Uint8Array): string {
	const row = w * 4 + 1;
	const idat = 6 + h * (5 + row);
	// prettier-ignore
	const bytes = [
		137, 80, 78, 71, 13, 10, 26, 10, 0, 0, 0, 13, 73, 72, 68, 82, 0, 0, w >> 8, w & 255, 0, 0, h >> 8,
		h & 255, 8, 6, 0, 0, 0, 0, 0, 0, 0, idat >>> 24, (idat >> 16) & 255, (idat >> 8) & 255, idat & 255,
		73, 68, 65, 84, 120, 1
	];
	let a = 1;
	let b = 0;
	for (let y = 0, i = 0, end = row - 1; y < h; y++, end += row - 1) {
		bytes.push(y + 1 < h ? 0 : 1, row & 255, row >> 8, ~row & 255, (row >> 8) ^ 255, 0);
		for (b = (b + a) % 65521; i < end; i++) {
			const u = rgba[i] & 255;
			bytes.push(u);
			a = (a + u) % 65521;
			b = (b + a) % 65521;
		}
	}
	// prettier-ignore
	bytes.push(
		b >> 8, b & 255, a >> 8, a & 255, 0, 0, 0, 0, 0, 0, 0, 0, 73, 69, 78, 68, 174, 66, 96, 130
	);
	for (const [start, end] of [
		[12, 29],
		[37, 41 + idat]
	]) {
		let c = ~0;
		for (let i = start; i < end; i++) {
			c ^= bytes[i];
			c = (c >>> 4) ^ CRC_TABLE[c & 15];
			c = (c >>> 4) ^ CRC_TABLE[c & 15];
		}
		c = ~c;
		bytes[end] = c >>> 24;
		bytes[end + 1] = (c >> 16) & 255;
		bytes[end + 2] = (c >> 8) & 255;
		bytes[end + 3] = c & 255;
	}
	let binary = '';
	for (const byte of bytes) binary += String.fromCharCode(byte);
	return 'data:image/png;base64,' + btoa(binary);
}

const cache = new Map<string, string | null>();

function decodeBase64(value: string): Uint8Array {
	const binary = atob(value);
	const out = new Uint8Array(binary.length);
	for (let i = 0; i < binary.length; i++) out[i] = binary.charCodeAt(i);
	return out;
}

/** A PNG data URL for a base64 ThumbHash, or null when it can't be decoded. Cached per hash. */
export function thumbHashToDataURL(base64: string): string | null {
	const cached = cache.get(base64);
	if (cached !== undefined) return cached;
	let url: string | null = null;
	try {
		const hash = decodeBase64(base64);
		if (hash.length >= 5) {
			const { w, h, rgba } = thumbHashToRGBA(hash);
			url = rgbaToDataURL(w, h, rgba);
		}
	} catch (err) {
		console.warn('[thumbhash] invalid hash', err);
	}
	cache.set(base64, url);
	return url;
}
