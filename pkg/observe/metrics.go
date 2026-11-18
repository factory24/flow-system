package observe

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"
)

var latencyBuckets = []float64{0.005, 0.025, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

type histogram struct {
	counts []uint64
	sum    float64
	n      uint64
}

type registry struct {
	mu       sync.Mutex
	counters map[string]map[string]uint64
	hists    map[string]map[string]*histogram
}

var reg = &registry{counters: map[string]map[string]uint64{}, hists: map[string]map[string]*histogram{}}

func labelString(kv []string) string {
	parts := make([]string, 0, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		parts = append(parts, fmt.Sprintf("%s=%q", kv[i], kv[i+1]))
	}
	return strings.Join(parts, ",")
}

// Count adds one to the counter name for the label pairs (key, value, key, value...).
func Count(name string, labels ...string) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	c := reg.counters[name]
	if c == nil {
		c = map[string]uint64{}
		reg.counters[name] = c
	}
	c[labelString(labels)]++
}

// Observe records a duration in the histogram name.
func Observe(name string, d time.Duration, labels ...string) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	h := reg.hists[name]
	if h == nil {
		h = map[string]*histogram{}
		reg.hists[name] = h
	}
	key := labelString(labels)
	e := h[key]
	if e == nil {
		e = &histogram{counts: make([]uint64, len(latencyBuckets))}
		h[key] = e
	}
	s := d.Seconds()
	for i, b := range latencyBuckets {
		if s <= b {
			e.counts[i]++
		}
	}
	e.sum += s
	e.n++
}

// JobRun records one run of a job and, when err is not nil, one failure.
func JobRun(job string, err error) {
	Count("job_runs_total", "job", job)
	if err != nil {
		Count("job_failures_total", "job", job)
	}
}

// ConsumerNack records a message a consumer could not process.
func ConsumerNack(topic string) { Count("consumer_nacks_total", "topic", topic) }

// OutboundError records a failed call to target.
func OutboundError(target string) { Count("outbound_call_errors_total", "target", target) }

func write(w io.Writer) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	names := make([]string, 0, len(reg.counters))
	for n := range reg.counters {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Fprintf(w, "# TYPE %s counter\n", n)
		for _, l := range sortedKeys(reg.counters[n]) {
			fmt.Fprintf(w, "%s%s %d\n", n, braces(l), reg.counters[n][l])
		}
	}
	names = names[:0]
	for n := range reg.hists {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Fprintf(w, "# TYPE %s histogram\n", n)
		keys := make([]string, 0, len(reg.hists[n]))
		for k := range reg.hists[n] {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, l := range keys {
			h := reg.hists[n][l]
			join := ""
			if l != "" {
				join = l + ","
			}
			for i, b := range latencyBuckets {
				fmt.Fprintf(w, "%s_bucket{%sle=\"%g\"} %d\n", n, join, b, h.counts[i])
			}
			fmt.Fprintf(w, "%s_bucket{%sle=\"+Inf\"} %d\n", n, join, h.n)
			fmt.Fprintf(w, "%s_sum%s %g\n%s_count%s %d\n", n, braces(l), h.sum, n, braces(l), h.n)
		}
	}
}

func braces(l string) string {
	if l == "" {
		return ""
	}
	return "{" + l + "}"
}

func sortedKeys(m map[string]uint64) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
