package main

// The workflow's python steps (day-18/workflow/*.py) hold the only atomic limit on showcase
// questions and the backstop that makes every question countable. They are deterministic and
// need no network, so they are tested here on temporary files (test review, wave 2).

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type scriptRun struct {
	code    int
	outputs string
	stdout  string
}

func runScript(t *testing.T, script string, env map[string]string) scriptRun {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatalf("python3 is required by the workflow and by this test: %v", err)
	}
	outputs := filepath.Join(t.TempDir(), "github_output")
	if err := os.WriteFile(outputs, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(python, filepath.Join("workflow", script))
	cmd.Env = append(os.Environ(), "GITHUB_OUTPUT="+outputs)
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out, err := cmd.CombinedOutput()
	code := 0
	if exit, ok := err.(*exec.ExitError); ok {
		code = exit.ExitCode()
	} else if err != nil {
		t.Fatalf("%s: %v", script, err)
	}
	raw, _ := os.ReadFile(outputs)
	return scriptRun{code: code, outputs: string(raw), stdout: string(out)}
}

func writeRecords(t *testing.T, path string, records []map[string]any) {
	t.Helper()
	raw, err := json.Marshal(records)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func readRecords(t *testing.T, path string) []map[string]any {
	t.Helper()
	var records []map[string]any
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &records); err != nil {
		t.Fatal(err)
	}
	return records
}

// recent returns n showcase records inside the last hour plus two that must not count: one
// without request_id (the owner's own question) and one older than an hour.
func recent(n int) []map[string]any {
	now := time.Now().UTC()
	records := []map[string]any{}
	for i := 0; i < n; i++ {
		records = append(records, map[string]any{"request_id": strings.Repeat("a", 32), "at": now.Add(-time.Duration(i+1) * time.Minute).Format(time.RFC3339)})
	}
	records = append(records,
		map[string]any{"request_id": "", "at": now.Format(time.RFC3339)},
		map[string]any{"request_id": strings.Repeat("b", 32), "at": now.Add(-2 * time.Hour).Format(time.RFC3339)},
	)
	return records
}

func TestWorkflowStrictLimitScript(t *testing.T) {
	id := strings.Repeat("c", 32)
	env := func(path, rid string) map[string]string {
		return map[string]string{"QUESTIONS": path, "QUESTIONS_PER_HOUR": "12", "REQUEST_ID": rid, "QUESTION": "-вопрос"}
	}
	t.Run("12 showcase questions in the hour: refusal recorded atomically, no run", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "questions.json")
		writeRecords(t, path, recent(12))
		got := runScript(t, "strict_limit.py", env(path, id))
		if got.code != 0 || !strings.Contains(got.outputs, "ask=false") || !strings.Contains(got.outputs, "full=true") {
			t.Fatalf("%+v", got)
		}
		records := readRecords(t, path)
		last := records[len(records)-1]
		if len(records) != 15 || last["request_id"] != id || last["error"] != "лимит вопросов на час исчерпан" || last["question"] != "-вопрос" {
			t.Fatalf("last=%v len=%d", last, len(records))
		}
		if leftovers, _ := filepath.Glob(filepath.Join(dir, "*.tmp")); len(leftovers) != 0 {
			t.Fatalf("temporary file left: %v", leftovers)
		}
	})
	t.Run("11 in the hour (owner's and old records do not count): the question runs", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "questions.json")
		writeRecords(t, path, recent(11))
		got := runScript(t, "strict_limit.py", env(path, id))
		if got.code != 0 || !strings.Contains(got.outputs, "ask=true") || strings.Contains(got.outputs, "full=") || len(readRecords(t, path)) != 13 {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("no file yet and no request_id", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "questions.json")
		if got := runScript(t, "strict_limit.py", env(path, id)); got.code != 0 || !strings.Contains(got.outputs, "ask=true") {
			t.Fatalf("%+v", got)
		}
		if got := runScript(t, "strict_limit.py", env(path, "")); got.code != 0 || !strings.Contains(got.outputs, "ask=true") {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("invalid request_id fails without the limit signal", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "questions.json")
		got := runScript(t, "strict_limit.py", env(path, "../../etc"))
		if got.code != 1 || strings.Contains(got.outputs, "full=") || strings.Contains(got.outputs, "ask=true") {
			t.Fatalf("%+v", got)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("a file was written for an invalid id: %v", err)
		}
	})
}

func TestWorkflowEnsureRecordedScript(t *testing.T) {
	id := strings.Repeat("d", 32)
	env := func(path string) map[string]string {
		return map[string]string{"QUESTIONS": path, "REQUEST_ID": id, "QUESTION": "q", "OUTCOME": "failure"}
	}
	t.Run("missing record is appended with the step outcome", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "questions.json")
		writeRecords(t, path, recent(1))
		if got := runScript(t, "ensure_recorded.py", env(path)); got.code != 0 {
			t.Fatalf("%+v", got)
		}
		records := readRecords(t, path)
		last := records[len(records)-1]
		if len(records) != 4 || last["request_id"] != id || last["error"] != "агент не записал ответ (шаг вопроса: failure)" {
			t.Fatalf("%v", last)
		}
		if leftovers, _ := filepath.Glob(filepath.Join(dir, "*.tmp")); len(leftovers) != 0 {
			t.Fatalf("temporary file left: %v", leftovers)
		}
	})
	t.Run("an existing record is left alone", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "questions.json")
		writeRecords(t, path, []map[string]any{{"request_id": id, "at": time.Now().UTC().Format(time.RFC3339), "answer": "ok"}})
		before, _ := os.ReadFile(path)
		if got := runScript(t, "ensure_recorded.py", env(path)); got.code != 0 {
			t.Fatalf("%+v", got)
		}
		after, _ := os.ReadFile(path)
		if string(before) != string(after) {
			t.Fatal("an existing record was rewritten")
		}
	})
	t.Run("no file yet", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "questions.json")
		if got := runScript(t, "ensure_recorded.py", env(path)); got.code != 0 || len(readRecords(t, path)) != 1 {
			t.Fatalf("%+v", got)
		}
	})
}
