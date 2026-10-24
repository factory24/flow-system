package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/factory24/flow-system/pkg/response"
	"golang.org/x/sync/singleflight"
)

const maxCachedResponses = 10000

type cachedBody struct {
	body    []byte
	expires time.Time
}

type responseCache struct {
	mu      sync.Mutex
	entries map[string]cachedBody
	now     func() time.Time
}

var (
	sharedResponses = &responseCache{entries: map[string]cachedBody{}, now: time.Now}
	inflight        singleflight.Group
)

func (rc *responseCache) get(key string) ([]byte, bool) {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	e, ok := rc.entries[key]
	if !ok {
		return nil, false
	}
	if !rc.now().Before(e.expires) {
		delete(rc.entries, key)
		return nil, false
	}
	return e.body, true
}

func (rc *responseCache) put(key string, body []byte, ttl time.Duration) {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if len(rc.entries) >= maxCachedResponses {
		now := rc.now()
		for k, e := range rc.entries {
			if !now.Before(e.expires) {
				delete(rc.entries, k)
			}
		}
		if len(rc.entries) >= maxCachedResponses {
			rc.entries = map[string]cachedBody{}
		}
	}
	rc.entries[key] = cachedBody{body: body, expires: rc.now().Add(ttl)}
}

// ForgetCached drops one URL from the response cache, e.g. right after the caller
// itself changed that resource.
func ForgetCached(url string) {
	sharedResponses.mu.Lock()
	delete(sharedResponses.entries, url)
	sharedResponses.mu.Unlock()
}

func cacheable(r Request) bool {
	return r.CacheTTL > 0 && r.Body == nil && (r.Method == "" || r.Method == http.MethodGet)
}

type sharedResult struct {
	body []byte
	err  error
}

// cachedDo keeps raw bytes and decodes a fresh value per caller, so no two callers
// ever share (and mutate) the same struct. Failures are never cached.
func cachedDo[T any](ctx context.Context, c *Client, r Request) (*response.ApiResponse[T], error) {
	if body, ok := sharedResponses.get(r.URL); ok {
		return decodeShared[T](body, nil, r)
	}
	v, _, _ := inflight.Do(r.URL, func() (any, error) {
		res, err := do[json.RawMessage](ctx, c, r)
		var body []byte
		if res != nil {
			body, _ = json.Marshal(res)
		}
		if err == nil && body != nil {
			sharedResponses.put(r.URL, body, r.CacheTTL)
		}
		return sharedResult{body: body, err: err}, nil
	})
	out := v.(sharedResult)
	return decodeShared[T](out.body, out.err, r)
}

func decodeShared[T any](body []byte, callErr error, r Request) (*response.ApiResponse[T], error) {
	if body == nil {
		return nil, callErr
	}
	res := new(response.ApiResponse[T])
	if err := json.Unmarshal(body, res); err != nil {
		if callErr != nil {
			return nil, callErr
		}
		return nil, fmt.Errorf("%s: answered with a body this service could not read: %w", describeCall(r.Method, r.URL), err)
	}
	return res, callErr
}
