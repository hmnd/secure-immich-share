package sanitize_test

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hmnd/secure-immich-share/server/internal/media/sanitize"
	"github.com/hmnd/secure-immich-share/server/internal/media/sanitize/sanitizetest"
)

// samplesDir holds real camera files from exif-py's test resources
// (https://github.com/ianare/exif-py/tree/master/tests/resources), fetched
// by `just fetch-samples`. They have mixed licences, so they aren't
// committed.
const samplesDir = "testdata/exif-py"

// sampleRejected are samples the sanitizer must refuse: broken files and
// formats it never strips (RAW, TIFF, JPEG XL are only served through
// Immich's derivatives).
var sampleRejected = map[string]string{
	"jpg/corrupted.jpg": "broken JPEG structure",
	"jxl/test_0001.jxl": "unsupported format",
	"raw/nikon_z_9_high_efficiency_compressed_dx_cropped_max_overexposed.dng": "RAW",
	"raw/nikon_z_9_high_efficiency_compressed_dx_cropped_max_overexposed.nef": "RAW",
	"raw/sony_alpha_a7iii_raw_image.ARW":                                      "RAW",
	"tiff/Arbitro.tiff":                                                       "TIFF",
	"tiff/BSG1.tiff":                                                          "TIFF",
	"tiff/Crémieux11.tiff":                                                    "TIFF",
	"tiff/DudleyLeavittUtah.tiff":                                             "TIFF",
	"tiff/Jobagent.tiff":                                                      "TIFF",
	"tiff/Picoawards.tiff":                                                    "TIFF",
	"tiff/Rudless.tiff":                                                       "TIFF",
	"tiff/Tless0.tiff":                                                        "TIFF",
}

// sampleUndecodable are inputs ffmpeg can't open, before or after
// stripping; stripping must still work, but pixels can't be compared.
var sampleUndecodable = map[string]bool{
	"heic/mobile/HMD_Nokia_8.3_5G_hdr.heif": true,
}

func samples(t *testing.T) []string {
	t.Helper()
	if _, err := os.Stat(filepath.Join(samplesDir, ".rev")); err != nil {
		t.Fatalf("exif-py samples missing: run `just fetch-samples`")
	}
	var out []string
	err := filepath.WalkDir(samplesDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		switch strings.ToLower(filepath.Ext(p)) {
		case ".txt", ".rst", ".rev":
			return nil
		}
		out = append(out, p)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// identifyingTags lists the input's tags that identify a person, place,
// time or device (a subset; the verifier is stricter).
func identifyingTags(t *testing.T, exiftool, p string) []string {
	t.Helper()
	out, _ := exec.Command(exiftool, "-j", "-a", "-G1", p).Output()
	var docs []map[string]any
	if json.Unmarshal(out, &docs) != nil || len(docs) != 1 {
		return nil
	}
	var found []string
	for name := range docs[0] {
		group, tag, _ := strings.Cut(name, ":")
		switch {
		case group == "GPS", strings.HasPrefix(tag, "GPS"), tag == "Make", tag == "Model",
			strings.HasPrefix(tag, "DateTimeOriginal"), strings.Contains(tag, "SerialNumber"),
			strings.HasPrefix(group, "XMP-") && group != "XMP-x" && group != "XMP-HDRGainMap",
			group == "MakerNotes" || strings.HasSuffix(group, "MakerNotes"):
			found = append(found, name)
		}
	}
	return found
}

func TestExifPySamples(t *testing.T) {
	t.Parallel()
	tools := sanitizetest.LookTools(t)
	for _, p := range samples(t) {
		name := filepath.ToSlash(strings.TrimPrefix(p, samplesDir+string(filepath.Separator)))
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := newRunner(t)
			ctx := context.Background()

			if ids := identifyingTags(t, tools.Exiftool, p); len(ids) > 0 {
				bad, err := r.Check(ctx, p)
				if err == nil && len(bad) == 0 {
					t.Fatalf("verifier accepted an input with %v", ids)
				}
			}

			out := r.Process(ctx, p, sanitize.OpStrip)
			if reason, ok := sampleRejected[name]; ok {
				if _, isRejected := out.(sanitize.Rejected); !isRejected {
					t.Fatalf("%s: got %T %v, want Rejected", reason, out, out)
				}
				return
			}
			v, ok := out.(sanitize.Verified)
			if !ok {
				t.Fatalf("process: %T %v", out, out)
			}
			if bad, err := r.Check(ctx, v.Path()); err != nil || len(bad) > 0 {
				t.Fatalf("re-check output: %v %v", bad, err)
			}
			if ids := identifyingTags(t, tools.Exiftool, v.Path()); len(ids) > 0 {
				t.Fatalf("output still has %v", ids)
			}
			kind, _ := sanitize.Sniff(p)
			if v.Container() != kind {
				t.Errorf("kind: got %s, want %s", v.Container(), kind)
			}
			if !sampleUndecodable[name] {
				if a, b := codedData(t, tools, p, kind), codedData(t, tools, v.Path(), v.Container()); string(a) != string(b) {
					t.Errorf("coded image data changed (%d → %d bytes)", len(a), len(b))
				}
			}
			for _, tag := range []string{"Orientation", "QuickTime:Rotation"} {
				a, b := tagValue(t, tools.Exiftool, p, tag), tagValue(t, tools.Exiftool, v.Path(), tag)
				// No orientation and 1 (normal) display the same.
				if a != b && (tag != "Orientation" || a != "" || b != "1") {
					t.Errorf("%s: %q → %q", tag, a, b)
				}
			}
			if !sampleUndecodable[name] {
				decodes(t, tools, v.Path())
			}
		})
	}
}

func tagValue(t *testing.T, exiftool, p, tag string) string {
	t.Helper()
	out, err := exec.Command(exiftool, "-s3", "-n", "-"+tag, p).Output()
	if err != nil {
		t.Fatalf("exiftool -%s: %v", tag, err)
	}
	return strings.TrimSpace(string(out))
}
