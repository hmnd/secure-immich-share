// Package ratelimit resolves client IPs behind trusted proxies and holds the
// request limiters: general per-IP token buckets and the unlock throttle.
package ratelimit

import (
	"math"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/hmnd/secure-immich-share/server/internal/clock"
)

// ClientIP is the visitor's address: X-Forwarded-For is read right to left,
// skipping trusted hops, but only when the connection itself comes from a
// trusted proxy; otherwise the socket address is used.
func ClientIP(r *http.Request, trusted []netip.Prefix) netip.Addr {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}
	}
	peer = peer.Unmap()
	if !isTrusted(peer, trusted) {
		return peer
	}
	hops := r.Header.Values("X-Forwarded-For")
	var all []string
	for _, h := range hops {
		all = append(all, strings.Split(h, ",")...)
	}
	for i := len(all) - 1; i >= 0; i-- {
		addr, err := netip.ParseAddr(strings.TrimSpace(all[i]))
		if err != nil {
			// A malformed hop ends the trusted chain.
			return peer
		}
		addr = addr.Unmap()
		if !isTrusted(addr, trusted) {
			return addr
		}
		peer = addr
	}
	return peer
}

// Key is the limiter key for a client address. An IPv6 client usually
// controls a whole /64, so IPv6 addresses are limited per /64.
func Key(a netip.Addr) string {
	if a.Is6() {
		p, _ := a.Prefix(64)
		return p.String()
	}
	return a.String()
}

func isTrusted(a netip.Addr, trusted []netip.Prefix) bool {
	for _, p := range trusted {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// Buckets is a set of token buckets keyed by string, dropped when idle.
type Buckets struct {
	clock clock.Clock
	limit rate.Limit
	burst int

	mu    sync.Mutex
	items map[string]*bucket
	sweep time.Time
}

type bucket struct {
	lim  *rate.Limiter
	seen time.Time
}

// NewBuckets makes buckets refilling at perSecond with the given burst.
func NewBuckets(c clock.Clock, perSecond float64, burst int) *Buckets {
	return &Buckets{clock: c, limit: rate.Limit(perSecond), burst: burst, items: map[string]*bucket{}}
}

// Allow takes a token for key; when empty it reports how long to wait.
func (b *Buckets) Allow(key string) (bool, time.Duration) { return b.AllowAll(key) }

// AllowAll takes a token from every key's bucket, or from none when any of
// them is empty; then it reports how long to wait.
func (b *Buckets) AllowAll(keys ...string) (bool, time.Duration) {
	now := b.clock.Now()
	b.mu.Lock()
	defer b.mu.Unlock()
	b.gc(now)
	taken := make([]*rate.Reservation, 0, len(keys))
	var wait time.Duration
	for _, key := range keys {
		it := b.items[key]
		if it == nil {
			it = &bucket{lim: rate.NewLimiter(b.limit, b.burst)}
			b.items[key] = it
		}
		it.seen = now
		r := it.lim.ReserveN(now, 1)
		taken = append(taken, r)
		wait = max(wait, r.DelayFrom(now))
	}
	if wait == 0 {
		return true, 0
	}
	for _, r := range slices.Backward(taken) {
		r.CancelAt(now)
	}
	return false, wait
}

// gc drops buckets idle long enough to be full again.
func (b *Buckets) gc(now time.Time) {
	if now.Sub(b.sweep) < time.Minute {
		return
	}
	b.sweep = now
	full := time.Duration(float64(b.burst) / float64(b.limit) * float64(time.Second))
	for k, it := range b.items {
		if now.Sub(it.seen) > full+time.Minute {
			delete(b.items, k)
		}
	}
}

// RetryAfterSeconds rounds a wait up to whole seconds (at least 1).
func RetryAfterSeconds(d time.Duration) int {
	return max(1, int(math.Ceil(d.Seconds())))
}
