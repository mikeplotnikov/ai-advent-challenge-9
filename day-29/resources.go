package main

import (
	"context"

	"os/exec"
	"strconv"
	"strings"
	"time"
)

type loadedModel struct {
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	VRAM    int64  `json:"size_vram"`
	Context int    `json:"context_length"`
}

type resourceSample struct {
	ObservedPS       []loadedModel `json:"observed_ps,omitempty"`
	RequestedContext int           `json:"requested_context_length,omitempty"`
	AtMS             int64         `json:"at_ms"`
	Size             int64         `json:"size"`
	VRAM             int64         `json:"size_vram"`
	Context          int           `json:"context_length"`
	RSS              int64         `json:"rss_kib"`
	PIDs             []int         `json:"runner_pids"`
	Error            string        `json:"error,omitempty"`
}
type resourceTrace struct {
	IntervalMS int              `json:"interval_ms"`
	Samples    []resourceSample `json:"samples"`
	RSSMax     int64            `json:"rss_max_kib"`
	MemoryMax  int64            `json:"memory_bytes"`
}

func (a *app) startResources(ctx context.Context) func() resourceTrace {
	if !a.collectResources {
		return func() resourceTrace { return resourceTrace{Samples: []resourceSample{}} }
	}
	stop := make(chan struct{})
	done := make(chan resourceTrace, 1)
	start := time.Now()
	go func() {
		trace := resourceTrace{IntervalMS: 250, Samples: []resourceSample{}}
		sample := func() {
			s := resourceSample{AtMS: time.Since(start).Milliseconds(), PIDs: []int{}}
			c, cancel := context.WithTimeout(ctx, time.Second)
			defer cancel()
			var ps struct {
				Models []loadedModel `json:"models"`
			}
			_, e := a.call(c, "/api/ps", nil, &ps)
			if e != nil {
				s.Error = e.Error()
			} else {
				selected := a.selectedAllocation(ps.Models)
				s.ObservedPS = selected.ObservedPS
				s.RequestedContext = selected.RequestedContext
				s.Size, s.VRAM, s.Context = selected.Size, selected.VRAM, selected.Context
			}
			pids, e := exec.CommandContext(c, "pgrep", "-f", "/ollama/llama-server|ollama runner").Output()
			if e == nil {
				for _, v := range strings.Fields(string(pids)) {
					pid, e := strconv.Atoi(v)
					if e != nil {
						continue
					}
					rss, e := exec.CommandContext(c, "ps", "-o", "rss=", "-p", v).Output()
					if e != nil {
						continue
					}
					n, e := strconv.ParseInt(strings.TrimSpace(string(rss)), 10, 64)
					if e == nil {
						s.RSS += n
						s.PIDs = append(s.PIDs, pid)
					}
				}
			}
			if s.RSS > trace.RSSMax {
				trace.RSSMax = s.RSS
			}
			if s.Size > trace.MemoryMax {
				trace.MemoryMax = s.Size
			}
			trace.Samples = append(trace.Samples, s)
		}
		sample()
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				sample()
				done <- trace
				return
			case <-ctx.Done():
				done <- trace
				return
			case <-ticker.C:
				sample()
			}
		}
	}()
	return func() resourceTrace { close(stop); return <-done }
}
func combineResources(attempts []attempt) resourceTrace {
	r := resourceTrace{IntervalMS: 250, Samples: []resourceSample{}}
	for _, a := range attempts {
		r.Samples = append(r.Samples, a.Resources.Samples...)
		if a.Resources.RSSMax > r.RSSMax {
			r.RSSMax = a.Resources.RSSMax
		}
		if a.Resources.MemoryMax > r.MemoryMax {
			r.MemoryMax = a.Resources.MemoryMax
		}
	}
	return r
}

// Preserve observations while excluding the previous profile during model reconfiguration.
func (a *app) selectedAllocation(models []loadedModel) resourceSample {
	requested := 16384
	if a.profile.ID != "" {
		requested = a.profile.Context
	}
	s := resourceSample{ObservedPS: models, RequestedContext: requested}
	for _, m := range models {
		if (m.Name == a.model || m.Name == a.model+":latest") && m.Context == requested {
			s.Size, s.VRAM, s.Context = m.Size, m.VRAM, m.Context
		}
	}
	return s
}
