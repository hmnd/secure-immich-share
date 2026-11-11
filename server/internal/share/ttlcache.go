package share

import "time"

// ttlCache is a map whose entries expire and which holds at most max of
// them, dropping an arbitrary one when full. It is not goroutine-safe.
type ttlCache[K comparable, V any] struct {
	max   int
	items map[K]ttlItem[V]
	swept time.Time
}

type ttlItem[V any] struct {
	v     V
	until time.Time
}

func newTTLCache[K comparable, V any](maxItems int) *ttlCache[K, V] {
	return &ttlCache[K, V]{max: maxItems, items: map[K]ttlItem[V]{}}
}

func (c *ttlCache[K, V]) get(k K, now time.Time) (V, bool) {
	it, ok := c.items[k]
	if !ok {
		return it.v, false
	}
	if !now.Before(it.until) {
		delete(c.items, k)
		var zero V
		return zero, false
	}
	return it.v, true
}

func (c *ttlCache[K, V]) put(k K, v V, until, now time.Time) {
	if now.Sub(c.swept) >= time.Minute {
		c.swept = now
		for k, it := range c.items {
			if !now.Before(it.until) {
				delete(c.items, k)
			}
		}
	}
	if _, ok := c.items[k]; !ok && len(c.items) >= c.max {
		for old := range c.items {
			delete(c.items, old)
			break
		}
	}
	c.items[k] = ttlItem[V]{v: v, until: until}
}

func (c *ttlCache[K, V]) delete(k K) { delete(c.items, k) }

func (c *ttlCache[K, V]) deleteFunc(del func(K, V) bool) {
	for k, it := range c.items {
		if del(k, it.v) {
			delete(c.items, k)
		}
	}
}
