package media

import (
	"fmt"
	"log/slog"
	"testing"
)

// A share whose run is scheduled survives the LRU trim, so its run starts.
func TestPrewarmTrimKeepsScheduled(t *testing.T) {
	p := newPrewarmer(slog.Default())
	if !p.fire("scheduled", prewarmEvent{kind: pwReady, listing: "l"}) {
		t.Fatal("not scheduled")
	}
	for i := range maxPrewarmShares + 10 {
		p.fire(fmt.Sprint("other-", i), prewarmEvent{kind: pwReady, listing: "l"})
		p.fire(fmt.Sprint("other-", i), prewarmEvent{kind: pwStart})
		p.fire(fmt.Sprint("other-", i), prewarmEvent{kind: pwSkipped})
	}
	if !p.fire("scheduled", prewarmEvent{kind: pwStart}) {
		t.Fatal("scheduled share was evicted")
	}
	if n := p.lru.Len(); n > maxPrewarmShares {
		t.Fatalf("%d shares kept", n)
	}
}
