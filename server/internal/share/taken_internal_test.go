package share

import (
	"testing"

	"github.com/hmnd/secure-immich-share/server/internal/immich"
)

func TestTakenAt(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, local, created, want string
	}{
		{"west", "2024-07-01T18:00:00.000Z", "2024-07-02T01:00:00.000Z", "2024-07-01T18:00:00-07:00"},
		{"half-hour", "2024-07-01T18:00:00.000Z", "2024-07-01T12:30:00.000Z", "2024-07-01T18:00:00+05:30"},
		{"utc", "2024-07-01T18:00:00.500Z", "2024-07-01T18:00:00.500Z", "2024-07-01T18:00:00Z"},
		{"no offset", "2024-07-01T18:00:00.000Z", "", "2024-07-01T18:00:00"},
		{"no local", "", "2024-07-01T18:00:00.000Z", ""},
	} {
		got := takenAt(takenOf(immich.Asset{LocalDateTime: c.local, FileCreatedAt: c.created}))
		if got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
	for _, created := range []string{"2024-07-01T12:30:00Z", "2024-07-01T12:30:00", "2024-07-01T12:30:00.123"} {
		if got := takenAt(bucketTaken(created, 5.5)); got != "2024-07-01T18:00:00+05:30" {
			t.Errorf("bucket %s: got %q", created, got)
		}
	}
}
