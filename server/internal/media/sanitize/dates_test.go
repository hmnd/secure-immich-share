package sanitize_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/hmnd/secure-immich-share/server/internal/media/sanitize"
	"github.com/hmnd/secure-immich-share/server/internal/media/sanitize/sanitizetest"
)

var taken = sanitize.Taken{
	Local: time.Date(2021, 8, 15, 18, 30, 45, 0, time.UTC),
	UTC:   time.Date(2021, 8, 16, 1, 30, 45, 0, time.UTC),
}

func readTags(t *testing.T, r *sanitize.Runner, p string) map[string]any {
	t.Helper()
	out, err := r.Exif(context.Background(), "-j", "-a", "-G1", p)
	if err != nil {
		t.Fatalf("exiftool: %v", err)
	}
	var tags []map[string]any
	if err := json.Unmarshal(out, &tags); err != nil || len(tags) != 1 {
		t.Fatalf("exiftool json: %v %s", err, out)
	}
	return tags[0]
}

// TestDatesWritten: with Taken, every op writes the date taken in place of
// the source's own dates, the output verifies only when dates are kept, and
// nothing else about the date is written.
func TestDatesWritten(t *testing.T) {
	t.Parallel()
	files := map[string]string{}
	for _, f := range sanitizetest.Corpus(t) {
		files[f.Name] = f.Path
	}
	cases := []struct {
		name, path string
		op         sanitize.Op
		want       map[string]string
	}{
		{"jpeg-strip", files["jpeg-gps"], sanitize.OpStrip, nil},
		{"webp-strip", sanitizetest.Clean(t, "clean.webp"), sanitize.OpStrip, nil},
		{"avif-strip", files["avif-gps"], sanitize.OpStrip, nil},
		{"heic-strip", files["heic-gps"], sanitize.OpStrip, nil},
		{"jpeg-verify", sanitizetest.Clean(t, "derivative.jpg"), sanitize.OpVerify, nil},
		{"webp-verify", sanitizetest.Clean(t, "derivative.webp"), sanitize.OpVerify, nil},
		{"mp4-remux", sanitizetest.Clean(t, "clean.mp4"), sanitize.OpRemux, map[string]string{
			"QuickTime:CreateDate": "2021:08:16 01:30:45", "QuickTime:ModifyDate": "2021:08:16 01:30:45",
			"Track1:TrackCreateDate": "2021:08:16 01:30:45", "Track1:MediaCreateDate": "2021:08:16 01:30:45",
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if c.path == "" {
				t.Fatal("missing corpus file")
			}
			r := newRunner(t)
			r.KeepDates = true
			out := r.Process(context.Background(), c.path, c.op, taken)
			v, ok := out.(sanitize.Verified)
			if !ok {
				t.Fatalf("process: %T %v", out, out)
			}
			want := c.want
			if want == nil {
				want = map[string]string{"ExifIFD:DateTimeOriginal": "2021:08:15 18:30:45"}
			}
			tags := readTags(t, r, v.Path())
			for name, val := range want {
				if tags[name] != val {
					t.Errorf("%s: got %v, want %q", name, tags[name], val)
				}
			}
			for _, name := range []string{"ExifIFD:OffsetTimeOriginal", "ExifIFD:SubSecTimeOriginal", "ExifIFD:CreateDate", "IFD0:ModifyDate"} {
				if v, ok := tags[name]; ok {
					t.Errorf("%s written: %v", name, v)
				}
			}
			p := copyTo(t, v.Path(), "dated"+filepath.Ext(v.Path()))
			mustReject(t, newRunner(t), p)
		})
	}
}

// TestDatesSkipPNG: PNGs can't carry the date taken (no eXIf chunk), so
// they are processed without it.
func TestDatesSkipPNG(t *testing.T) {
	t.Parallel()
	r := newRunner(t)
	r.KeepDates = true
	out := r.Process(context.Background(), sanitizetest.Clean(t, "clean.png"), sanitize.OpStrip, taken)
	v, ok := out.(sanitize.Verified)
	if !ok {
		t.Fatalf("process: %T %v", out, out)
	}
	if d, ok := readTags(t, r, v.Path())["ExifIFD:DateTimeOriginal"]; ok {
		t.Errorf("date written: %v", d)
	}
}

// TestDatesOnlyDateTaken: keeping dates allows the date taken, not the
// other EXIF dates and time zones.
func TestDatesOnlyDateTaken(t *testing.T) {
	t.Parallel()
	tools := sanitizetest.LookTools(t)
	r := newRunner(t)
	r.KeepDates = true
	base := verifiedBase(t, r, "clean.jpg", sanitize.OpStrip)
	for _, tag := range []string{
		"-CreateDate=2024:05:01 10:00:00", "-ModifyDate=2024:05:01 10:00:00", "-OffsetTimeOriginal=-07:00",
		"-SubSecTimeOriginal=123", "-GPSDateStamp=2024:05:01", "-XMP-exif:DateTimeOriginal=2024:05:01 10:00:00",
	} {
		t.Run(tag, func(t *testing.T) {
			t.Parallel()
			p := copyTo(t, base, "m.jpg")
			writeTag(t, tools.Exiftool, p, tag)
			mustReject(t, r, p)
		})
	}
}
