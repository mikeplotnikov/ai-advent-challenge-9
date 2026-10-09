package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

func (a *app) cold(path, id string) error {
	p, e := findProfile(id)
	if e != nil {
		return e
	}
	f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	defer f.Close()
	if _, e = a.pinned(context.Background(), p); e != nil {
		return e
	}
	meta, e := a.configured(p).config(context.Background())
	if e != nil {
		return e
	}
	for _, model := range []string{"qwen3:4b", "qwen3:4b-thinking-2507-q8_0", "bge-m3"} {
		var res map[string]any
		if _, e = a.call(context.Background(), "/api/generate", map[string]any{"model": model, "keep_alive": 0}, &res); e != nil {
			return e
		}
	}
	var ps map[string]any
	if _, e = a.call(context.Background(), "/api/ps", nil, &ps); e != nil {
		return e
	}
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	r := a.configured(p).runQuestion(ctx, "Что такое системный промпт?", func(string, any) {})
	value := map[string]any{"meta": meta, "profile": p, "before_ps": ps, "started_at": start.UTC().Format(time.RFC3339Nano), "wall_ms": time.Since(start).Milliseconds(), "result": r, "note": "После выгрузки обеих генеративных моделей и embedding. Отдельный контроль, не включён в тёплую серию."}
	if e = json.NewEncoder(f).Encode(value); e != nil {
		return e
	}
	if r.Error != "" {
		return fmt.Errorf("cold control: %s", r.Error)
	}
	return nil
}
