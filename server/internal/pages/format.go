package pages

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/a-h/templ"

	"github.com/hmnd/secure-immich-share/server/internal/httpapi/contract"
)

// parseTaken reads a takenAt local time, with or without its UTC offset.
func parseTaken(takenAt *string) (t time.Time, zoned, ok bool) {
	if takenAt == nil {
		return time.Time{}, false, false
	}
	if t, err := time.Parse(time.RFC3339, *takenAt); err == nil {
		return t, true, true
	}
	t, err := time.Parse("2006-01-02T15:04:05", *takenAt)
	return t, false, err == nil
}

// formatDate formats a takenAt as "1 July 2024"; empty if it isn't one.
func formatDate(takenAt *string) string {
	t, _, ok := parseTaken(takenAt)
	if !ok {
		return ""
	}
	return t.Format("2 January 2006")
}

// formatDateTime formats a takenAt as "1 July 2024 at 18:00 (UTC-7)", without
// the zone when it's unknown; empty if it isn't one.
func formatDateTime(takenAt *string) string {
	t, zoned, ok := parseTaken(takenAt)
	if !ok {
		return ""
	}
	s := t.Format("2 January 2006 at 15:04")
	if zoned {
		_, off := t.Zone()
		s += " (" + utcOffset(off) + ")"
	}
	return s
}

// utcOffset formats an offset in seconds as "UTC", "UTC-7" or "UTC+5:30".
func utcOffset(off int) string {
	if off == 0 {
		return "UTC"
	}
	sign := "+"
	if off < 0 {
		sign, off = "-", -off
	}
	s := "UTC" + sign + strconv.Itoa(off/3600)
	if m := off % 3600 / 60; m != 0 {
		s += fmt.Sprintf(":%02d", m)
	}
	return s
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
