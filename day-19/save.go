package main

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/toolagent"
)

func writeJSONAtomic(path string, value any) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".day19-json-*")
	if err != nil {
		return err
	}
	tmp := file.Name()
	cleanup := func() { _ = os.Remove(tmp) }
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		_ = file.Close()
		cleanup()
		return err
	}
	if err := file.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		cleanup()
		return err
	}
	return nil
}

func saveTrace(path string, trace toolagent.Trace) error { return writeJSONAtomic(path, trace) }
