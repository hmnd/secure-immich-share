package media

import (
	"container/list"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/hmnd/secure-immich-share/server/internal/media/cache"
	"github.com/hmnd/secure-immich-share/server/internal/media/sanitize"
	"github.com/hmnd/secure-immich-share/server/internal/share"
)

// Prewarm state, one per share:
//
//	idle → scheduled{listingHash} → running → done{listingHash, until}
//	                                        ↘ skipped → idle

type prewarmPhase string

const (
	pwIdle      prewarmPhase = "idle"
	pwScheduled prewarmPhase = "scheduled"
	pwRunning   prewarmPhase = "running"
	pwDone      prewarmPhase = "done"
)

type prewarmState struct {
	phase   prewarmPhase
	listing string
	until   time.Time
}

type prewarmEventKind string

const (
	pwReady    prewarmEventKind = "ready"    // a visitor got the share's listing
	pwStart    prewarmEventKind = "start"    // the prewarm run starts
	pwFinished prewarmEventKind = "finished" // every request was queued
	pwSkipped  prewarmEventKind = "skipped"  // some requests were skipped under load
)

type prewarmEvent struct {
	kind    prewarmEventKind
	listing string
	now     time.Time
	ttl     time.Duration
}

// prewarmTransition is the only place prewarm states change; ok=false
// means the event changes nothing (already scheduled, running or done).
func prewarmTransition(s prewarmState, e prewarmEvent) (prewarmState, bool, error) {
	switch e.kind {
	case pwReady:
		switch {
		case s.phase == pwIdle,
			s.phase == pwDone && (s.listing != e.listing || !e.now.Before(s.until)):
			return prewarmState{phase: pwScheduled, listing: e.listing}, true, nil
		case s.phase == pwScheduled, s.phase == pwRunning, s.phase == pwDone:
			return s, false, nil
		}
	case pwStart:
		if s.phase == pwScheduled {
			return prewarmState{phase: pwRunning, listing: s.listing}, true, nil
		}
	case pwFinished:
		if s.phase == pwRunning {
			return prewarmState{phase: pwDone, listing: s.listing, until: e.now.Add(e.ttl)}, true, nil
		}
	case pwSkipped:
		if s.phase == pwRunning {
			return prewarmState{phase: pwIdle}, true, nil
		}
	}
	return s, false, fmt.Errorf("media: illegal prewarm event %s in state %s", e.kind, s.phase)
}

const maxPrewarmShares = 1000

type prewarmer struct {
	log    *slog.Logger
	mu     sync.Mutex
	states map[string]*list.Element // share id → element holding *pwEntry
	lru    *list.List
}

type pwEntry struct {
	shareID string
	st      prewarmState
}

func newPrewarmer(log *slog.Logger) *prewarmer {
	return &prewarmer{log: log, states: map[string]*list.Element{}, lru: list.New()}
}

func (p *prewarmer) fire(shareID string, e prewarmEvent) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	el := p.states[shareID]
	if el == nil {
		el = p.lru.PushFront(&pwEntry{shareID: shareID, st: prewarmState{phase: pwIdle}})
		p.states[shareID] = el
		p.trimLocked(el)
	}
	p.lru.MoveToFront(el)
	ent := el.Value.(*pwEntry)
	next, ok, err := prewarmTransition(ent.st, e)
	if err != nil {
		p.log.Error("media: prewarm state machine", "error", err)
		return false
	}
	ent.st = next
	return ok
}

// trimLocked forgets the least recently used shares beyond
// maxPrewarmShares, except keep. Scheduled and running shares are kept too:
// their run fires further events, which must not land on a fresh idle entry.
func (p *prewarmer) trimLocked(keep *list.Element) {
	for el := p.lru.Back(); el != nil && p.lru.Len() > maxPrewarmShares; {
		prev := el.Prev()
		if ent := el.Value.(*pwEntry); el != keep && ent.st.phase != pwScheduled && ent.st.phase != pwRunning {
			p.lru.Remove(el)
			delete(p.states, ent.shareID)
		}
		el = prev
	}
}

// Prewarm queues the share's videos (and images when enabled) at prewarm
// priority after a visitor got its listing. Call it only for public or
// unlocked shares.
func (m *Service) Prewarm(sh *share.Share) {
	if !m.o.PrewarmVideos && !m.o.PrewarmImages {
		return
	}
	now := m.o.Clock.Now()
	if !m.prewarm.fire(sh.ID, prewarmEvent{kind: pwReady, listing: sh.ListingHash, now: now}) {
		return
	}
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		if !m.prewarm.fire(sh.ID, prewarmEvent{kind: pwStart}) {
			return
		}
		skipped := m.prewarmRun(sh)
		kind := pwFinished
		if skipped {
			kind = pwSkipped
		}
		m.prewarm.fire(sh.ID, prewarmEvent{kind: kind, now: m.o.Clock.Now(), ttl: m.o.PrewarmTTL})
	}()
}

// prewarmRun requests every prewarm variant and reports whether any was
// skipped (load, Immich unavailable or shutdown).
func (m *Service) prewarmRun(sh *share.Share) bool {
	skipped := false
	for _, it := range itemsOf(sh) {
		var variants []sanitize.Variant
		if m.o.PrewarmVideos && (it.Kind == share.ItemVideo || it.Kind == share.ItemLive) {
			variants = append(variants, sanitize.VariantVideo)
		}
		if m.o.PrewarmImages {
			variants = append(variants, sanitize.VariantThumb, sanitize.VariantPreview)
		}
		for _, v := range variants {
			if m.base.Err() != nil {
				return true
			}
			p, err := m.plan(m.base, sh, it.ID, v)
			if err != nil {
				if errors.Is(err, ErrUnavailable) {
					skipped = true
				}
				continue
			}
			if _, err := m.cache.Start(p.key, cache.Prewarm, p.job); err != nil {
				skipped = true
			}
		}
	}
	return skipped
}
