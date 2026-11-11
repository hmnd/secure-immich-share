package contract

import (
	"encoding/json"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
)

// MediaVariants are the on-demand variants.
var MediaVariants = []string{"full", "video", "download"}

// ShareView is a share a visitor may see. It is built from allowlisted
// fields only: no filenames, camera, location or people.
type ShareView struct {
	Kind          string      `json:"kind" validate:"oneof=album individual"`
	Title         *string     `json:"title"`
	Description   *string     `json:"description"`
	ExpiresAt     *time.Time  `json:"expiresAt"`
	AllowDownload bool        `json:"allowDownload"`
	ZipAvailable  bool        `json:"zipAvailable"`
	CoverID       *string     `json:"coverId"`
	Items         []ShareItem `json:"items"`
}

// ShareItem is one photo or video.
type ShareItem struct {
	ID        string  `json:"id"`
	Kind      string  `json:"kind" validate:"oneof=image video live"`
	Ratio     float64 `json:"ratio"`
	Thumbhash *string `json:"thumbhash"`
	// TakenAt is the local date and time (YYYY-MM-DDTHH:MM:SS, no time
	// zone) or null.
	TakenAt    *string        `json:"takenAt"`
	ThumbURL   string         `json:"thumbUrl"`
	PreviewURL string         `json:"previewUrl"`
	Variants   []MediaVariant `json:"variants"`
}

// MediaVariant is an on-demand variant.
type MediaVariant string

func (MediaVariant) SetSchema(s *openapi3.Schema) {
	*s = *enumStr(MediaVariants...).Value
}

// AssetDetail is the lightbox detail of one item.
type AssetDetail struct {
	Description *string `json:"description"`
	TakenAt     *string `json:"takenAt"`
}

// MediaStatus is the state of an on-demand variant.
type MediaStatus struct{ v any }

// MediaReady: the variant can be loaded from url.
func MediaReady(url string) MediaStatus {
	return MediaStatus{v: struct {
		State string `json:"state"`
		URL   string `json:"url"`
	}{"ready", url}}
}

// MediaPending: queued, processing or busy; poll again after retryAfterMs.
func MediaPending(state string, retryAfterMs int64) MediaStatus {
	return MediaStatus{v: struct {
		State        string `json:"state"`
		RetryAfterMs int64  `json:"retryAfterMs"`
	}{state, retryAfterMs}}
}

// MediaFailed: the variant can't be produced.
func MediaFailed() MediaStatus {
	return MediaStatus{v: struct {
		State string `json:"state"`
	}{"failed"}}
}

func (m MediaStatus) MarshalJSON() ([]byte, error) { return json.Marshal(m.v) }

func (MediaStatus) SetSchema(s *openapi3.Schema) {
	union(s, "state",
		object(map[string]*openapi3.SchemaRef{"state": constStr("ready"), "url": str()}, "state", "url"),
		object(map[string]*openapi3.SchemaRef{
			"state": enumStr("queued", "processing", "busy"), "retryAfterMs": integer(),
		}, "state", "retryAfterMs"),
		object(map[string]*openapi3.SchemaRef{"state": constStr("failed")}, "state"),
	)
}

// StartZipRequest picks the items to zip; without a body, the zip holds the
// whole share. Entries keep their share position names.
type StartZipRequest struct {
	AssetIDs []string `json:"assetIds"`
}

// ZipStatus is the state of a zip job.
type ZipStatus struct{ v any }

// ZipPreparing: entries are being prepared.
func ZipPreparing(id string, done, total int) ZipStatus {
	return ZipStatus{v: struct {
		State string `json:"state"`
		ZipID string `json:"zipId"`
		Done  int    `json:"done"`
		Total int    `json:"total"`
	}{"preparing", id, done, total}}
}

// ZipReady: the zip can be downloaded from url.
func ZipReady(id, url string, included, skipped int, size int64) ZipStatus {
	return ZipStatus{v: struct {
		State    string `json:"state"`
		ZipID    string `json:"zipId"`
		URL      string `json:"url"`
		Included int    `json:"included"`
		Skipped  int    `json:"skipped"`
		Size     int64  `json:"size"`
	}{"ready", id, url, included, skipped, size}}
}

// ZipFailed: nothing could be included.
func ZipFailed() ZipStatus {
	return ZipStatus{v: struct {
		State string `json:"state"`
	}{"failed"}}
}

func (z ZipStatus) MarshalJSON() ([]byte, error) { return json.Marshal(z.v) }

func (ZipStatus) SetSchema(s *openapi3.Schema) {
	union(s, "state",
		object(map[string]*openapi3.SchemaRef{
			"state": constStr("preparing"), "zipId": str(), "done": integer(), "total": integer(),
		}, "state", "zipId", "done", "total"),
		object(map[string]*openapi3.SchemaRef{
			"state": constStr("ready"), "zipId": str(), "url": str(), "included": integer(),
			"skipped": integer(), "size": integer(),
		}, "state", "zipId", "url", "included", "skipped", "size"),
		object(map[string]*openapi3.SchemaRef{"state": constStr("failed")}, "state"),
	)
}
