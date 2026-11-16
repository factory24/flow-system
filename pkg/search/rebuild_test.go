package search

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
)

type fakeCluster struct {
	mu      sync.Mutex
	indices map[string]bool
	aliases map[string]map[string]bool
}

func newFake() *fakeCluster {
	return &fakeCluster{indices: map[string]bool{}, aliases: map[string]map[string]bool{}}
}

func (f *fakeCluster) names(alias string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []string{}
	for i := range f.aliases[alias] {
		out = append(out, i)
	}
	sort.Strings(out)
	return out
}

func (f *fakeCluster) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := strings.TrimPrefix(r.URL.Path, "/")
	switch {
	case r.Method == http.MethodPut:
		f.indices[p] = true
	case r.Method == http.MethodDelete:
		if !f.indices[p] {
			http.Error(w, "missing", http.StatusNotFound)
			return
		}
		delete(f.indices, p)
		for _, set := range f.aliases {
			delete(set, p)
		}
	case strings.HasSuffix(p, "/_refresh"):
	case p == "_aliases":
		var body struct {
			Actions []map[string]struct{ Index, Alias string } `json:"actions"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		for _, a := range body.Actions {
			if v, ok := a["remove_index"]; ok {
				delete(f.indices, v.Index)
			}
			if v, ok := a["remove"]; ok {
				delete(f.aliases[v.Alias], v.Index)
			}
			if v, ok := a["add"]; ok {
				if f.indices[v.Alias] {
					http.Error(w, "alias clashes with index", http.StatusBadRequest)
					return
				}
				if f.aliases[v.Alias] == nil {
					f.aliases[v.Alias] = map[string]bool{}
				}
				f.aliases[v.Alias][v.Index] = true
			}
		}
	case r.Method == http.MethodGet && strings.HasSuffix(p, "/_count"):
		_, _ = w.Write([]byte(`{"count":7}`))
		return
	case r.Method == http.MethodGet:
		out := map[string]any{}
		if f.indices[p] {
			out[p] = map[string]any{"aliases": map[string]any{}}
		}
		for i := range f.aliases[p] {
			out[i] = map[string]any{"aliases": map[string]any{p: map[string]any{}}}
		}
		if len(out) == 0 {
			http.Error(w, "missing", http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(out)
		return
	}
	_, _ = w.Write([]byte("{}"))
}

func TestRebuildSwapsAliasAndDeletesOld(t *testing.T) {
	f := newFake()
	srv := httptest.NewServer(f)
	defer srv.Close()
	c := &HTTPCluster{URL: srv.URL}
	var first, second string
	if err := Rebuild(context.Background(), c, "v-things", nil, func(_ context.Context, target string) error { first = target; return nil }); err != nil {
		t.Fatal(err)
	}
	if got := f.names("v-things"); len(got) != 1 || got[0] != first {
		t.Fatalf("alias = %v, want [%s]", got, first)
	}
	if err := Rebuild(context.Background(), c, "v-things", nil, func(_ context.Context, target string) error {
		second = target
		if got := f.names("v-things"); len(got) != 1 || got[0] != first {
			t.Fatalf("during rebuild alias = %v, want old index %s", got, first)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if first == second || f.indices[first] || !f.indices[second] {
		t.Fatalf("old %s should be gone, new %s present: %v", first, second, f.indices)
	}
	if got := f.names("v-things"); len(got) != 1 || got[0] != second {
		t.Fatalf("alias = %v, want [%s]", got, second)
	}
}

func TestRebuildFailureKeepsOldIndex(t *testing.T) {
	f := newFake()
	srv := httptest.NewServer(f)
	defer srv.Close()
	c := &HTTPCluster{URL: srv.URL}
	var first string
	_ = Rebuild(context.Background(), c, "v-things", nil, func(_ context.Context, target string) error { first = target; return nil })
	boom := errors.New("boom")
	err := Rebuild(context.Background(), c, "v-things", nil, func(context.Context, string) error { return boom })
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if got := f.names("v-things"); len(got) != 1 || got[0] != first || len(f.indices) != 1 {
		t.Fatalf("alias %v indices %v", got, f.indices)
	}
}

func TestRebuildReplacesConcreteIndex(t *testing.T) {
	f := newFake()
	f.indices["v-things"] = true
	srv := httptest.NewServer(f)
	defer srv.Close()
	var target string
	err := Rebuild(context.Background(), &HTTPCluster{URL: srv.URL}, "v-things", nil, func(_ context.Context, t string) error { target = t; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if f.indices["v-things"] || len(f.names("v-things")) != 1 || !f.indices[target] {
		t.Fatalf("indices %v alias %v", f.indices, f.names("v-things"))
	}
}

func TestRebuildAgainstElasticsearch(t *testing.T) {
	esURL := os.Getenv("ES_TEST_URL")
	if esURL == "" {
		t.Skip("ES_TEST_URL not set")
	}
	c := &HTTPCluster{URL: esURL}
	ctx := context.Background()
	name := "rebuild-it-" + strings.ToLower(t.Name())
	put := func(target, id string) error {
		return c.Do(ctx, http.MethodPut, "/"+target+"/_doc/"+id+"?refresh=true", []byte(`{"n":"`+id+`"}`), nil)
	}
	count := func() int {
		var res struct{ Count int }
		if err := c.Do(ctx, http.MethodGet, "/"+name+"/_count", nil, &res); err != nil {
			t.Fatal(err)
		}
		return res.Count
	}
	t.Cleanup(func() { _ = c.Do(ctx, http.MethodDelete, "/"+name+"-*", nil, nil) })
	if err := Rebuild(ctx, c, name, nil, func(ctx context.Context, target string) error {
		for _, id := range []string{"a", "b"} {
			if err := put(target, id); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := count(); got != 2 {
		t.Fatalf("count = %d, want 2", got)
	}
	err := Rebuild(ctx, c, name, nil, func(ctx context.Context, target string) error {
		if got := count(); got != 2 {
			t.Errorf("read during rebuild = %d, want old rows (2)", got)
		}
		for _, id := range []string{"a", "b", "c"} {
			if err := put(target, id); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := count(); got != 3 {
		t.Fatalf("count after swap = %d, want 3", got)
	}
}

type fakeBus struct {
	topic, kind string
	payload     any
	n           int
}

func (b *fakeBus) PublishEvent(topic, kind string, payload any) error {
	b.topic, b.kind, b.payload = topic, kind, payload
	b.n++
	return nil
}

func TestRebuildPublishesSearchRebuilt(t *testing.T) {
	f := newFake()
	srv := httptest.NewServer(f)
	defer srv.Close()
	bus := &fakeBus{}
	SetPublisher(bus)
	defer SetPublisher(nil)
	c := &HTTPCluster{URL: srv.URL}
	if err := Rebuild(context.Background(), c, "v-things", nil, func(context.Context, string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	ev, ok := bus.payload.(Rebuilt)
	if bus.n != 1 || bus.topic != "search" || bus.kind != "search.rebuilt" || !ok || ev.Alias != "v-things" || ev.Docs != 7 || !strings.HasPrefix(ev.Index, "v-things-") || ev.At == "" {
		t.Fatalf("bus %+v payload %+v", bus, bus.payload)
	}
	_ = Rebuild(context.Background(), c, "v-things", nil, func(context.Context, string) error { return errors.New("boom") })
	if bus.n != 1 {
		t.Fatalf("a failed rebuild published: %d", bus.n)
	}
}
