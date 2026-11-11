package cache

import "context"

// Peek returns the state of k without starting anything.
func (c *Cache) Peek(k Key) State {
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.getLocked(k)
	st := e.st
	c.dropAbsentLocked(e)
	return st
}

// Wait blocks until k's job (if any) ends or ctx is done.
func (c *Cache) Wait(ctx context.Context, k Key) {
	c.mu.Lock()
	var done chan struct{}
	if e := c.entries[k]; e != nil && (e.st.Phase == Queued || e.st.Phase == Running) {
		done = e.done
	}
	c.mu.Unlock()
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
		}
	}
}

type Counters struct {
	Entries, VisitorQueue, PrewarmQueue int
	Bytes                               int64
}

func (c *Cache) Counters() Counters {
	c.mu.Lock()
	defer c.mu.Unlock()
	return Counters{Entries: c.lru.Len(), VisitorQueue: len(c.visitorQ), PrewarmQueue: len(c.prewarmQ), Bytes: c.total}
}
