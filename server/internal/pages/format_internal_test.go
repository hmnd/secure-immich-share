package pages

import "testing"

func TestFormatDateTime(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"2024-07-01T18:00:00-07:00": "1 July 2024 at 18:00 (UTC-7)",
		"2024-07-01T18:00:00+05:30": "1 July 2024 at 18:00 (UTC+5:30)",
		"2024-07-01T18:00:00Z":      "1 July 2024 at 18:00 (UTC)",
		"2024-07-01T18:00:00":       "1 July 2024 at 18:00",
		"2024-07-01":                "",
	} {
		if got := formatDateTime(&in); got != want {
			t.Errorf("%s: got %q, want %q", in, got, want)
		}
	}
}
