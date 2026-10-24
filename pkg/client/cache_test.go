package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type user struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func userServer(t *testing.T, status int) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		time.Sleep(20 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if status >= 300 {
			_, _ = w.Write([]byte(`{"success":false,"errors":["user not found"]}`))
			return
		}
		_, _ = w.Write([]byte(`{"success":true,"data":{"id":"b79f1934","name":"Vendor"}}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func resetCache(now func() time.Time) {
	sharedResponses = &responseCache{entries: map[string]cachedBody{}, now: now}
}

func TestACachedLookupHitsTheServiceOnceWithinTheTTL(t *testing.T) {
	resetCache(time.Now)
	srv, hits := userServer(t, http.StatusOK)
	c := &Client{HTTP: srv.Client()}
	req := Request{Method: http.MethodGet, URL: srv.URL + "/v1/users/b79f1934", Public: true, CacheTTL: time.Minute}

	for i := 0; i < 5; i++ {
		res, err := Do[*user](context.Background(), c, req)
		if err != nil || res.Data == nil || res.Data.Name != "Vendor" {
			t.Fatalf("call %d: %+v %v", i, res, err)
		}
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("want 1 upstream call for 5 lookups, got %d", n)
	}
}

func TestConcurrentMissesAreCollapsedIntoOneCall(t *testing.T) {
	resetCache(time.Now)
	srv, hits := userServer(t, http.StatusOK)
	c := &Client{HTTP: srv.Client()}
	req := Request{URL: srv.URL + "/v1/users/b79f1934", Public: true, CacheTTL: time.Minute}

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := Do[*user](context.Background(), c, req); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if n := hits.Load(); n != 1 {
		t.Fatalf("want the 20 concurrent misses collapsed into 1 call, got %d", n)
	}
}

func TestEachCallerGetsItsOwnCopy(t *testing.T) {
	resetCache(time.Now)
	srv, _ := userServer(t, http.StatusOK)
	c := &Client{HTTP: srv.Client()}
	req := Request{URL: srv.URL + "/v1/users/b79f1934", Public: true, CacheTTL: time.Minute}

	first, _ := Do[*user](context.Background(), c, req)
	first.Data.Name = "mutated by caller"
	second, _ := Do[*user](context.Background(), c, req)
	if second.Data.Name != "Vendor" {
		t.Fatalf("a caller's change leaked into the cache: %q", second.Data.Name)
	}
}

func TestAnEntryExpiresAfterItsTTL(t *testing.T) {
	now := time.Now()
	resetCache(func() time.Time { return now })
	srv, hits := userServer(t, http.StatusOK)
	c := &Client{HTTP: srv.Client()}
	req := Request{URL: srv.URL + "/v1/users/b79f1934", Public: true, CacheTTL: 30 * time.Second}

	_, _ = Do[*user](context.Background(), c, req)
	now = now.Add(31 * time.Second)
	_, _ = Do[*user](context.Background(), c, req)
	if n := hits.Load(); n != 2 {
		t.Fatalf("want a fresh call after the TTL, got %d calls", n)
	}
}

func TestFailuresAreNotCachedAndKeepTheirErrorBody(t *testing.T) {
	resetCache(time.Now)
	srv, hits := userServer(t, http.StatusNotFound)
	c := &Client{HTTP: srv.Client()}
	req := Request{URL: srv.URL + "/v1/users/missing", Public: true, CacheTTL: time.Minute}

	for i := 0; i < 2; i++ {
		res, err := Do[*user](context.Background(), c, req)
		if err == nil {
			t.Fatal("want the 404 as an error")
		}
		if res == nil || len(res.Errors) != 1 || res.Errors[0] != "user not found" {
			t.Fatalf("the API's error body should still reach the caller: %+v", res)
		}
	}
	if n := hits.Load(); n != 2 {
		t.Fatalf("failures must not be cached, got %d calls", n)
	}
}

func TestWithoutACacheTTLNothingIsCached(t *testing.T) {
	resetCache(time.Now)
	srv, hits := userServer(t, http.StatusOK)
	c := &Client{HTTP: srv.Client()}
	req := Request{URL: srv.URL + "/v1/users/b79f1934", Public: true}
	_, _ = Do[*user](context.Background(), c, req)
	_, _ = Do[*user](context.Background(), c, req)
	if n := hits.Load(); n != 2 || len(sharedResponses.entries) != 0 {
		t.Fatalf("opt-in only: got %d calls, %d cached", n, len(sharedResponses.entries))
	}
}

func TestForgetCachedDropsOneEntry(t *testing.T) {
	resetCache(time.Now)
	srv, hits := userServer(t, http.StatusOK)
	c := &Client{HTTP: srv.Client()}
	req := Request{URL: srv.URL + "/v1/users/b79f1934", Public: true, CacheTTL: time.Minute}
	_, _ = Do[*user](context.Background(), c, req)
	ForgetCached(req.URL)
	_, _ = Do[*user](context.Background(), c, req)
	if n := hits.Load(); n != 2 {
		t.Fatalf("want a fresh call after ForgetCached, got %d", n)
	}
}
