package sanitize_test

import (
	"testing"

	"github.com/hmnd/secure-immich-share/server/internal/media/sanitize/sanitizetest"
)

func TestMain(m *testing.M) {
	defer sanitizetest.RemoveCorpus()
	m.Run()
}
