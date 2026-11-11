// Package sanitize turns raw Immich bytes into files that are proven free of
// identifying metadata. Verified, the only handle the cache and the HTTP
// layer accept, can only be built here after the verifier passed.
package sanitize

// Variant is what a visitor asks for.
type Variant string

const (
	VariantThumb    Variant = "thumb"
	VariantPreview  Variant = "preview"
	VariantFull     Variant = "full"
	VariantDownload Variant = "download"
	VariantVideo    Variant = "video"
)

// Format is the class of an asset's original file, from Immich's
// originalMimeType and asset type. It only selects the source chain; output
// files are always sniffed from their bytes.
type Format string

const (
	FormatWeb   Format = "web"   // JPEG, PNG, WebP
	FormatHEIF  Format = "heif"  // HEIC, HEIF, AVIF
	FormatOther Format = "other" // RAW, DNG, TIFF, GIF and anything else
	FormatVideo Format = "video"
)

// ClassifyMime maps an original MIME type onto a Format.
func ClassifyMime(mime string, isVideo bool) Format {
	if isVideo {
		return FormatVideo
	}
	switch mime {
	case "image/jpeg", "image/png", "image/webp":
		return FormatWeb
	case "image/heic", "image/heif", "image/avif":
		return FormatHEIF
	}
	return FormatOther
}

// Source is an Immich endpoint the bytes come from.
type Source string

const (
	SourceOriginal  Source = "original"  // /assets/{id}/original
	SourceFullsize  Source = "fullsize"  // /assets/{id}/thumbnail?size=fullsize
	SourcePreview   Source = "preview"   // /assets/{id}/thumbnail?size=preview
	SourceThumbnail Source = "thumbnail" // /assets/{id}/thumbnail?size=thumbnail
	SourcePlayback  Source = "playback"  // /assets/{id}/video/playback
	SourceMotion    Source = "motion"    // playback of the live photo's livePhotoVideoId
)

// Op is what happens to a source's bytes.
type Op string

const (
	OpStrip  Op = "strip"  // exiftool lossless strip, then verify
	OpVerify Op = "verify" // verify as is (Immich derivatives)
	OpRemux  Op = "remux"  // ffmpeg stream copy without metadata, then verify
)

// Step is one entry of a source chain.
type Step struct {
	Source Source
	Op     Op
}

func (s Step) String() string { return string(s.Source) + "→" + string(s.Op) }

// Chain is the source chain for an asset's format and a variant: the
// steps tried in order. A live photo's still uses its image format; live
// only adds the motion part as the video variant. An edited image's
// original lacks its edits, so its full size comes from Immich's render.
// Nil means the variant doesn't exist for the asset.
func Chain(f Format, live, edited bool, v Variant) []Step {
	if edited && f != FormatVideo && (v == VariantFull || v == VariantDownload) {
		return []Step{{SourceFullsize, OpVerify}, {SourcePreview, OpVerify}}
	}
	switch v {
	case VariantThumb:
		return []Step{{SourceThumbnail, OpVerify}, {SourceThumbnail, OpStrip}}
	case VariantPreview:
		return []Step{{SourcePreview, OpVerify}, {SourcePreview, OpStrip}}
	case VariantVideo:
		switch {
		case f == FormatVideo:
			return []Step{{SourcePlayback, OpRemux}}
		case live:
			return []Step{{SourceMotion, OpRemux}}
		}
	case VariantFull:
		switch f {
		case FormatWeb:
			return []Step{{SourceOriginal, OpStrip}, {SourceFullsize, OpVerify}, {SourcePreview, OpVerify}}
		case FormatHEIF, FormatOther:
			return []Step{{SourceFullsize, OpVerify}, {SourcePreview, OpVerify}}
		}
	case VariantDownload:
		switch f {
		case FormatWeb:
			return []Step{{SourceOriginal, OpStrip}}
		case FormatHEIF:
			return []Step{{SourceOriginal, OpStrip}, {SourceFullsize, OpVerify}}
		case FormatOther:
			return []Step{{SourceFullsize, OpVerify}, {SourcePreview, OpVerify}}
		case FormatVideo:
			return []Step{{SourcePlayback, OpRemux}}
		}
	}
	return nil
}
