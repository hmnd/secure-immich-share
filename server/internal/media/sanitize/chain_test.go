package sanitize

import "testing"

func TestForbiddenOverridesAllowlist(t *testing.T) {
	// Even if an allowlist entry were too broad, forbidden names fail.
	for _, name := range []string{
		"GPS:GPSLatitude", "IFD0:Make", "IFD0:Model", "ExifIFD:SerialNumber", "XMP-dc:Creator",
		"Keys:GPSCoordinates", "UserData:Make", "ItemList:Author", "Trailer:Anything", "File:EmbeddedVideo",
		"ExifIFD:DateTimeOriginal", "Composite:GPSPosition", "File:Comment", "ExifTool:Error",
		"MakerNotes:Anything", "Apple:RunTime", "Google:Anything", "Samsung:Anything",
		"QuickTime:LocationInformation", "IFD0:Software",
	} {
		if !isForbidden(name) {
			t.Errorf("%s: not forbidden", name)
		}
	}
	for _, name := range []string{"System:FileModifyDate", "ICC-header:ProfileDateTime", "QuickTime:CreateDate"} {
		if isForbidden(name) {
			t.Errorf("%s: forbidden but exempt", name)
		}
	}
}
