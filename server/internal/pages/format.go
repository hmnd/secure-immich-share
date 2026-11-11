package pages

import (
	"strconv"
	"strings"
	"time"

	"github.com/a-h/templ"

	"github.com/hmnd/secure-immich-share/server/internal/httpapi/contract"
)

// formatDate formats a YYYY-MM-DD local date as "1 July 2024"; empty if it
// isn't one.
func formatDate(date *string) string {
	if date == nil {
		return ""
	}
	t, err := time.Parse(time.DateOnly, *date)
	if err != nil {
		return ""
	}
	return t.Format("2 January 2006")
}

func isVideo(it contract.ShareItem) bool { return it.Kind == "video" }

// altText is "Photo, 1 July 2024", "Video" and so on.
func altText(it contract.ShareItem) string {
	noun := "Photo"
	if isVideo(it) {
		noun = "Video"
	}
	if date := formatDate(it.TakenAt); date != "" {
		return noun + ", " + date
	}
	return noun
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

// countSummary is "12 photos · 3 videos".
func countSummary(items []contract.ShareItem) string {
	videos := 0
	for _, it := range items {
		if isVideo(it) {
			videos++
		}
	}
	photos := len(items) - videos
	var parts []string
	if photos > 0 || videos == 0 {
		parts = append(parts, plural(photos, "photo", "photos"))
	}
	if videos > 0 {
		parts = append(parts, plural(videos, "video", "videos"))
	}
	return strings.Join(parts, " · ")
}

// The grid clamps aspect ratios like the islands' layout.
const (
	minRatio = 0.25
	maxRatio = 4
)

func clampRatio(r float64) float64 { return min(maxRatio, max(minRatio, r)) }

func ratioStyle(name string, r float64) templ.SafeCSS {
	return templ.SafeCSS(name + ":" + strconv.FormatFloat(r, 'f', 4, 64))
}
