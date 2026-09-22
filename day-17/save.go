package main

import (
	"encoding/json"
	"os"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/toolagent"
)

func saveTrace(path string, trace toolagent.Trace) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	return encoder.Encode(trace)
}
