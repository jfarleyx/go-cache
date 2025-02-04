package cache

// modified version of https://raw.githubusercontent.com/patrickmn/go-cache
// made the following changes...
// 1. the ability to keep expired items in the cache and return expired items when asked for
// 2. change expiration management from per-key to entire cache
// 3. execute a callback function when the cache expires
// 4. removed increment/decrement
// 5. removed ability to persist cache to disk
// 6. removed auto eviction of expired items

import (
	"fmt"
	"runtime"
	"sync"
	"time"
)

// Item represents an item in the cache.
type Item struct {
	Object     interface{}
	Expiration int64
}

// Expired returns true if the item has expired.
func (item Item) Expired() bool {
	if item.Expiration == 0 {
		return false
	}
	return time.Now().UnixNano() > item.Expiration
}

// Cache is the cache object
type Cache struct {
	*cache
	// If this is confusing, see the comment at the bottom of New()
}

type cache struct {
	expiration time.Duration
	items      map[string]Item
	mu         sync.RWMutex
	onExpired  func()
	janitor    *janitor
}

// Set add an item to the cache, replacing any existing item.
func (c *cache) Set(k string, x interface{}) {
	// "Inlining" of set
	var e = time.Now().Add(c.expiration).UnixNano()

	c.mu.Lock()
	c.items[k] = Item{
		Object:     x,
		Expiration: e,
	}
	// TODO: Calls to mu.Unlock are currently not deferred because defer
	// adds ~200 ns (as of go1.)
	c.mu.Unlock()
}

func (c *cache) set(k string, x interface{}) {
	var e = time.Now().Add(c.expiration).UnixNano()
	c.items[k] = Item{
		Object:     x,
		Expiration: e,
	}
}

// Replace set a new value for the cache key only if it already exists. Returns an error otherwise.
func (c *cache) Replace(k string, x interface{}) error {
	c.mu.Lock()
	_, found := c.get(k)
	if !found {
		c.mu.Unlock()
		return fmt.Errorf("Item %s doesn't exist", k)
	}
	c.set(k, x)
	c.mu.Unlock()
	return nil
}

// Get an item from the cache. Returns the item or nil, and a bool indicating
// whether the key was found.
func (c *cache) Get(k string) (interface{}, bool) {
	c.mu.RLock()
	item, found := c.items[k]
	if !found {
		c.mu.RUnlock()
		return nil, false
	}
	c.mu.RUnlock()
	return item.Object, true
}

func (c *cache) get(k string) (interface{}, bool) {
	item, found := c.items[k]
	if !found {
		return nil, false
	}
	return item.Object, true
}

// Delete an item from the cache. Does nothing if the key is not in the cache.
func (c *cache) Delete(k string) {
	c.mu.Lock()
	delete(c.items, k)
	c.mu.Unlock()
}

// Delete all expired items from the cache.
func (c *cache) DeleteExpired() {
	now := time.Now().UnixNano()
	c.mu.Lock()
	for k, v := range c.items {
		if v.Expiration > 0 && now > v.Expiration {
			delete(c.items, k)
		}
	}
	c.mu.Unlock()
}

type keyAndValue struct {
	key   string
	value interface{}
}

// OnExpired sets an (optional) function that is called when the cache expires
func (c *cache) OnExpired(f func()) {
	c.onExpired = f
}

// Returns the number of items in the cache, including expired items.
func (c *cache) ItemCount() int {
	c.mu.RLock()
	n := len(c.items)
	c.mu.RUnlock()
	return n
}

// Delete all items from the cache.
func (c *cache) Flush() {
	c.mu.Lock()
	c.items = map[string]Item{}
	c.mu.Unlock()
}

type janitor struct {
	Interval time.Duration
	stop     chan bool
}

// handleExpired is fired by the ticker and executes the onExpired function.
func (c *cache) handleExpired() {
	if c.onExpired != nil {
		c.onExpired()
	}
}

func (j *janitor) Run(c *cache) {
	ticker := time.NewTicker(j.Interval)
	for {
		select {
		case <-ticker.C:
			c.handleExpired()
		case <-j.stop:
			ticker.Stop()
			return
		}
	}
}

func stopJanitor(c *Cache) {
	c.janitor.stop <- true
}

func runJanitor(c *cache, ex time.Duration) {
	j := &janitor{
		Interval: ex,
		stop:     make(chan bool, 1),
	}
	c.janitor = j
	go j.Run(c)
}

func newCache(ex time.Duration, m map[string]Item) *cache {
	if ex <= 0 {
		ex = -1
	}
	c := &cache{
		expiration: ex,
		items:      m,
	}
	return c
}

func newCacheWithJanitor(ex time.Duration, m map[string]Item) *Cache {
	c := newCache(ex, m)
	// This trick ensures that the janitor goroutine (which--granted it
	// was enabled--is running DeleteExpired on c forever) does not keep
	// the returned C object from being garbage collected. When it is
	// garbage collected, the finalizer stops the janitor goroutine, after
	// which c can be collected.
	C := &Cache{c}
	if ex > 0 {
		runJanitor(c, ex)
		runtime.SetFinalizer(C, stopJanitor)
	}
	return C
}

// New return a new cache with a given expiration duration. If the
// expiration duration is less than 1 (i.e. No Expiration),
// the items in the cache never expire (by default), and must be deleted
// manually. The OnExpired callback method is ignored, too.
func New(expiration time.Duration) *Cache {
	items := make(map[string]Item)
	return newCacheWithJanitor(expiration, items)
}
