package immich

import (
	"encoding/base64"
	"time"

	"github.com/hmnd/secure-immich-share/server/internal/media/sanitize/sanitizetest"
)

// Fixture asset ids.
const (
	PhotoID   = "a0000000-0000-4000-8000-000000000001" // JPEG with GPS; dirty thumbnail
	HEICID    = "a0000000-0000-4000-8000-000000000002" // HEIC with GPS
	VideoID   = "a0000000-0000-4000-8000-000000000003" // untranscoded MP4 with GPS
	LiveID    = "a0000000-0000-4000-8000-000000000004" // live photo still (HEIC)
	MotionID  = "a0000000-0000-4000-8000-000000000005" // its motion part (hidden)
	RawID     = "a0000000-0000-4000-8000-000000000006" // DNG stand-in
	BrokenID  = "a0000000-0000-4000-8000-000000000007" // every source fails verification
	TrashedID = "a0000000-0000-4000-8000-000000000008"
	LockedID  = "a0000000-0000-4000-8000-000000000009"
	OutsideID = "a0000000-0000-4000-8000-00000000000a" // in no share
)

// Fixture shares.
const (
	PublicShareID     = "b0000000-0000-4000-8000-000000000001"
	PasswordShareID   = "b0000000-0000-4000-8000-000000000002"
	NoDownloadShareID = "b0000000-0000-4000-8000-000000000003"
	IndividualShareID = "b0000000-0000-4000-8000-000000000004"

	PublicAlbumID     = "c0000000-0000-4000-8000-000000000001"
	PasswordAlbumID   = "c0000000-0000-4000-8000-000000000002"
	NoDownloadAlbumID = "c0000000-0000-4000-8000-000000000003"

	PublicSlug    = "summer-2024"
	PasswordSlug  = "private-2024"
	SharePassword = "hunter2"
)

// Key returns a deterministic 50-byte share key for n.
func Key(n byte) string {
	b := make([]byte, 50)
	for i := range b {
		b[i] = n
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// Fixture keys.
var (
	PublicKey     = Key(1)
	PasswordKey   = Key(2)
	NoDownloadKey = Key(3)
	IndividualKey = Key(4)
)

// AlbumAssets are the assets of every album fixture, in Immich's order.
var AlbumAssets = []string{PhotoID, HEICID, VideoID, LiveID, RawID, BrokenID, TrashedID, LockedID}

// Seed adds the standard fixtures, backed by the sanitizer corpus.
func (f *Immich) Seed(t sanitizetest.TB) {
	t.Helper()
	sanitizetest.Corpus(t)
	file := func(name string) string { return sanitizetest.Clean(t, name) }
	dirty := func(name string) string { return sanitizetest.ByName(t, name).Path }
	djpg, dwebp := file("derivative.jpg"), file("derivative.webp")
	day := func(d int) time.Time { return time.Date(2024, 7, d, 18, 0, 0, 0, time.UTC) }

	f.AddAsset(Asset{ID: PhotoID, Type: "IMAGE", Mime: "image/jpeg", Checksum: "photo", Width: 320, Height: 240,
		LocalDateTime: day(1), Description: "Jane at home",
		Original: dirty("jpeg-gps"), Preview: dirty("webp-xmp"), Thumbnail: dirty("jpeg-gps")})
	f.AddAsset(Asset{ID: HEICID, Type: "IMAGE", Mime: "image/heic", Checksum: "heic", Width: 320, Height: 240,
		LocalDateTime: day(2), Thumbhash: "1QcSHQRnh493V4dIh4eXh1h4kJUI",
		Original: dirty("heic-gps"), Fullsize: djpg, Preview: djpg, Thumbnail: dwebp})
	f.AddAsset(Asset{ID: VideoID, Type: "VIDEO", Mime: "video/mp4", Checksum: "video", Width: 320, Height: 240,
		LocalDateTime: day(3), Playback: dirty("mp4-gps-track"), Preview: djpg, Thumbnail: dwebp})
	f.AddAsset(Asset{ID: LiveID, Type: "IMAGE", Mime: "image/heic", Checksum: "live", Width: 240, Height: 320,
		LocalDateTime: day(4), LivePhotoVideoID: MotionID,
		Original: dirty("heic-gps"), Fullsize: djpg, Preview: djpg, Thumbnail: dwebp})
	f.AddAsset(Asset{ID: MotionID, Type: "VIDEO", Mime: "video/quicktime", Checksum: "motion", Visibility: "hidden",
		LocalDateTime: day(4), Playback: dirty("mov-rotated")})
	f.AddAsset(Asset{ID: RawID, Type: "IMAGE", Mime: "image/x-adobe-dng", Checksum: "raw", Width: 320, Height: 240,
		LocalDateTime: day(5), Original: dirty("tiff"), Fullsize: djpg, Preview: djpg, Thumbnail: dwebp})
	f.AddAsset(Asset{ID: BrokenID, Type: "IMAGE", Mime: "image/jpeg", Checksum: "broken", Width: 320, Height: 240,
		LocalDateTime: day(6), Original: dirty("tiff"), Preview: dirty("tiff"), Thumbnail: dirty("tiff")})
	f.AddAsset(Asset{ID: TrashedID, Type: "IMAGE", Mime: "image/jpeg", Checksum: "trashed", Trashed: true,
		LocalDateTime: day(7), Original: djpg, Preview: djpg, Thumbnail: djpg})
	f.AddAsset(Asset{ID: LockedID, Type: "IMAGE", Mime: "image/jpeg", Checksum: "locked", Visibility: "locked",
		LocalDateTime: day(8), Original: djpg, Preview: djpg, Thumbnail: djpg})
	f.AddAsset(Asset{ID: OutsideID, Type: "IMAGE", Mime: "image/jpeg", Checksum: "outside",
		Original: djpg, Preview: djpg, Thumbnail: djpg})

	desc := "Our summer"
	f.AddShare(Share{ID: PublicShareID, Key: PublicKey, Slug: PublicSlug, Album: true, AlbumID: PublicAlbumID,
		AlbumName: "Summer 2024", AlbumDesc: desc, AllowDownload: true, ShowMetadata: true, AssetIDs: AlbumAssets})
	f.AddShare(Share{ID: PasswordShareID, Key: PasswordKey, Slug: PasswordSlug, Password: SharePassword, Album: true, AlbumID: PasswordAlbumID,
		AlbumName: "Private", AllowDownload: true, ShowMetadata: true, AssetIDs: AlbumAssets})
	f.AddShare(Share{ID: NoDownloadShareID, Key: NoDownloadKey, Album: true, AlbumID: NoDownloadAlbumID,
		AlbumName: "View only", AllowDownload: false, ShowMetadata: true, AssetIDs: AlbumAssets})
	f.AddShare(Share{ID: IndividualShareID, Key: IndividualKey, AllowDownload: true, ShowMetadata: true,
		Description: &desc, AssetIDs: []string{PhotoID, VideoID}})
}
