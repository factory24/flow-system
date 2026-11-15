// Package search rebuilds Elasticsearch indices without a window in which readers see an empty or partial index.
package search

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Cluster is the minimal HTTP surface Rebuild needs; *HTTPCluster implements it.
type Cluster interface {
	Do(ctx context.Context, method, path string, body []byte, out any) error
}

// HTTPCluster talks to a cluster over its REST API with basic auth or an API key.
type HTTPCluster struct {
	URL, User, Password, APIKey string
	Client                      *http.Client
}

type StatusError struct {
	Method, Path string
	Status       int
	Body         string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("search %s %s: %d %s", e.Method, e.Path, e.Status, e.Body)
}

func (c *HTTPCluster) Do(ctx context.Context, method, path string, body []byte, out any) error {
	hc := c.Client
	if hc == nil {
		hc = &http.Client{Timeout: 60 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.URL, "/")+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.User != "" {
		req.SetBasicAuth(c.User, c.Password)
	} else if c.APIKey != "" {
		req.Header.Set("Authorization", "ApiKey "+c.APIKey)
	}
	res, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 64<<20))
	if res.StatusCode >= 300 {
		msg := string(raw)
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return &StatusError{Method: method, Path: path, Status: res.StatusCode, Body: msg}
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

func notFound(err error) bool {
	var se *StatusError
	return errors.As(err, &se) && se.Status == http.StatusNotFound
}

// Rebuild fills a fresh index `<index>-<timestamp>` through build, then points the alias `<index>` at it in one atomic
// step and deletes the indices the alias used to serve. Readers of `<index>` keep the old rows until the swap and never see
// a partial index. When build fails the new index is deleted and the alias is left untouched. A concrete index that still
// carries the alias name (created before aliases were used) is replaced in the same atomic step.
func Rebuild(ctx context.Context, c Cluster, index string, mappings json.RawMessage, build func(ctx context.Context, target string) error) error {
	target := fmt.Sprintf("%s-%s", index, time.Now().UTC().Format("20060102t150405.000000000"))
	target = strings.Replace(target, ".", "", 1)

	create := []byte("{}")
	if len(mappings) > 0 {
		create = mappings
	}
	if err := c.Do(ctx, http.MethodPut, "/"+url.PathEscape(target), create, nil); err != nil {
		return fmt.Errorf("create %s: %w", target, err)
	}
	abandon := func() {
		dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		_ = c.Do(dctx, http.MethodDelete, "/"+url.PathEscape(target), nil, nil)
	}
	if err := build(ctx, target); err != nil {
		abandon()
		return fmt.Errorf("build %s: %w", target, err)
	}
	if err := c.Do(ctx, http.MethodPost, "/"+url.PathEscape(target)+"/_refresh", nil, nil); err != nil {
		abandon()
		return fmt.Errorf("refresh %s: %w", target, err)
	}

	old, concrete, err := current(ctx, c, index)
	if err != nil {
		abandon()
		return err
	}
	actions := []map[string]any{}
	if concrete {
		actions = append(actions, map[string]any{"remove_index": map[string]any{"index": index}})
	}
	for _, o := range old {
		actions = append(actions, map[string]any{"remove": map[string]any{"index": o, "alias": index}})
	}
	actions = append(actions, map[string]any{"add": map[string]any{"index": target, "alias": index}})
	body, _ := json.Marshal(map[string]any{"actions": actions})
	if err := c.Do(ctx, http.MethodPost, "/_aliases", body, nil); err != nil {
		abandon()
		return fmt.Errorf("swap alias %s: %w", index, err)
	}
	for _, o := range old {
		if o == target {
			continue
		}
		if err := c.Do(ctx, http.MethodDelete, "/"+url.PathEscape(o), nil, nil); err != nil && !notFound(err) {
			return fmt.Errorf("delete old index %s: %w", o, err)
		}
	}
	return nil
}

// current returns the indices the alias serves, and whether `<index>` is itself a concrete index.
func current(ctx context.Context, c Cluster, index string) (indices []string, concrete bool, err error) {
	var res map[string]struct {
		Aliases map[string]json.RawMessage `json:"aliases"`
	}
	err = c.Do(ctx, http.MethodGet, "/"+url.PathEscape(index)+"?filter_path=*.aliases", nil, &res)
	if notFound(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("inspect %s: %w", index, err)
	}
	for name, v := range res {
		if name == index {
			concrete = true
			continue
		}
		if _, ok := v.Aliases[index]; ok {
			indices = append(indices, name)
		}
	}
	return indices, concrete, nil
}
