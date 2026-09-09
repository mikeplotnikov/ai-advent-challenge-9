// Package e2e runs the day-06 binary as a separate process, twice, to check the one
// thing day 7 is about: "начните диалог, перезапустите приложение, продолжите диалог
// и убедитесь, что агент помнит прошлые сообщения".
//
// It is a package of its own rather than another file next to main.go because day 6's
// encapsulation test forbids that directory from importing net/http and encoding/json
// at all — and a fake provider needs both. The guard stays intact; the test lives one
// directory down.
//
// The provider is faked, so the check is free and deterministic and can run on every
// `go test`. What it does not fake is the restart: two real processes, a real file
// between them, the real flags. A "restart" simulated inside one process would prove
// that a struct can be rebuilt, which was never the question.
package e2e

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// provider is a stand-in for the model that records every conversation sent to it.
type provider struct {
	mu       sync.Mutex
	requests [][]message
	answers  []string
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func (p *provider) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []message `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		p.mu.Lock()
		n := len(p.requests)
		p.requests = append(p.requests, body.Messages)
		answer := "хорошо"
		if n < len(p.answers) {
			answer = p.answers[n]
		}
		p.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":"e2e","model":"deepseek-v4-flash",
			"choices":[{"message":{"role":"assistant","content":%q},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12,
			"prompt_cache_hit_tokens":0,"prompt_cache_miss_tokens":10}}`, answer)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// calls is how many requests the model has been asked to answer. Day 8 needs it to
// state that a refusal cost nothing: the count must not move.
func (p *provider) calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.requests)
}

func (p *provider) request(t *testing.T, i int) []message {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if i >= len(p.requests) {
		t.Fatalf("запросов к модели %d, запрошен %d-й", len(p.requests), i+1)
	}
	return p.requests[i]
}

// build compiles the binary under test once per test.
func build(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "day06")
	cmd := exec.Command("go", "build", "-o", bin, "github.com/mikeplotnikov/ai-advent-challenge-9/day-06")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("сборка day-06: %v\n%s", err, out)
	}
	return bin
}

// run starts the binary as its own process and returns what it printed.
func run(t *testing.T, bin, url, workdir string, args ...string) (stdout, stderr string) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	// The working directory is empty on purpose: the child must not pick up the
	// repository's real .env and start talking to the real provider.
	cmd.Dir = workdir
	cmd.Env = append(os.Environ(),
		"DEEPSEEK_API_URL="+url,
		"DEEPSEEK_API_KEY=e2e-фальшивый-ключ",
		"DEEPSEEK_MODEL=deepseek-v4-flash",
	)
	var out, errb strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("запуск %v: %v\nstdout: %s\nstderr: %s", args, err, out.String(), errb.String())
	}
	return out.String(), errb.String()
}

func contentsOf(ms []message) string {
	parts := make([]string, len(ms))
	for i, m := range ms {
		parts[i] = m.Role + ":" + m.Content
	}
	return strings.Join(parts, " | ")
}

// The task's own check, performed the way it is written.
func TestTheAgentRemembersAcrossARestart(t *testing.T) {
	p := &provider{answers: []string{"приятно познакомиться, Михаил", "тебя зовут Михаил"}}
	srv := p.start(t)
	bin := build(t)
	work := t.TempDir()
	sessions := filepath.Join(work, "sessions")

	// Run one: a new process, an empty history.
	_, firstErr := run(t, bin, srv.URL, work,
		"-store-dir", sessions, "-session", "e2e", "меня зовут Михаил")
	if !strings.Contains(firstErr, "история пуста") {
		t.Errorf("первый запуск не сообщил, что история пуста:\n%s", firstErr)
	}

	file := filepath.Join(sessions, "e2e.json")
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("после первого запуска истории нет на диске: %v", err)
	}

	// The process is gone. Run two is a different one.
	stdout, secondErr := run(t, bin, srv.URL, work,
		"-store-dir", sessions, "-session", "e2e", "как меня зовут")
	if !strings.Contains(secondErr, "загружено ходов: 1") {
		t.Errorf("второй запуск не поднял историю:\n%s", secondErr)
	}
	if !strings.Contains(stdout, "Михаил") {
		t.Errorf("ответ второго запуска: %q", stdout)
	}

	// The proof is in what the second process sent, not in what it printed: the
	// model was told the first conversation.
	sent := p.request(t, 1)
	if len(sent) != 4 {
		t.Fatalf("второй запуск отправил %d сообщений, ожидалось 4: %s", len(sent), contentsOf(sent))
	}
	want := []string{"system", "user", "assistant", "user"}
	for i, role := range want {
		if sent[i].Role != role {
			t.Fatalf("роли второго запроса: %s", contentsOf(sent))
		}
	}
	if !strings.Contains(sent[1].Content, "меня зовут Михаил") {
		t.Errorf("вопрос первого запуска не уехал в модель: %s", contentsOf(sent))
	}
	if !strings.Contains(sent[2].Content, "Михаил") {
		t.Errorf("ответ первого запуска не уехал в модель: %s", contentsOf(sent))
	}
}

// The negative control, and the half of the demo that makes the other half mean
// something: the same two runs with memory switched off must send no history at all.
// Without this, a test that passes because the model happened to answer "Михаил"
// looks exactly like a test that passes because the store works.
func TestWithoutMemoryTheRestartedAgentSendsNoHistory(t *testing.T) {
	p := &provider{answers: []string{"приятно познакомиться", "не знаю"}}
	srv := p.start(t)
	bin := build(t)
	work := t.TempDir()
	sessions := filepath.Join(work, "sessions")

	_, first := run(t, bin, srv.URL, work, "-no-memory", "-store-dir", sessions, "меня зовут Михаил")
	if !strings.Contains(first, "память: выключена") {
		t.Errorf("первый запуск не сказал, что память выключена:\n%s", first)
	}
	run(t, bin, srv.URL, work, "-no-memory", "-store-dir", sessions, "как меня зовут")

	if _, err := os.Stat(sessions); !os.IsNotExist(err) {
		t.Errorf("с -no-memory на диске что-то появилось: %v", err)
	}
	sent := p.request(t, 1)
	if len(sent) != 2 {
		t.Fatalf("без памяти отправлено %d сообщений, ожидалось 2: %s", len(sent), contentsOf(sent))
	}
	for _, m := range sent {
		if strings.Contains(m.Content, "меня зовут Михаил") {
			t.Fatalf("без памяти в запрос просочилась прошлая беседа: %s", contentsOf(sent))
		}
	}
}

// Two sessions, two conversations, one binary. This is the second half of the demo:
// the agent that knows the name and the one that does not are the same program run
// with a different -session.
func TestSessionsStayApartAcrossRestarts(t *testing.T) {
	p := &provider{answers: []string{"запомнил", "не знаю"}}
	srv := p.start(t)
	bin := build(t)
	work := t.TempDir()
	sessions := filepath.Join(work, "sessions")

	run(t, bin, srv.URL, work, "-store-dir", sessions, "-session", "работа", "меня зовут Михаил")
	run(t, bin, srv.URL, work, "-store-dir", sessions, "-session", "дом", "как меня зовут")

	sent := p.request(t, 1)
	if len(sent) != 2 {
		t.Fatalf("вторая беседа отправила %d сообщений, ожидалось 2: %s", len(sent), contentsOf(sent))
	}
	entries, err := os.ReadDir(sessions)
	if err != nil {
		t.Fatalf("чтение каталога бесед: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("файлов бесед %d, ожидалось 2: %v", len(entries), entries)
	}
}

// -forget is the lever for starting over without deleting anything by hand. What it
// must not do is leave the old conversation somewhere it can come back from.
func TestForgetDropsTheStoredConversation(t *testing.T) {
	p := &provider{answers: []string{"запомнил", "не знаю"}}
	srv := p.start(t)
	bin := build(t)
	work := t.TempDir()
	sessions := filepath.Join(work, "sessions")

	run(t, bin, srv.URL, work, "-store-dir", sessions, "-session", "e2e", "меня зовут Михаил")
	_, stderr := run(t, bin, srv.URL, work, "-store-dir", sessions, "-session", "e2e", "-forget", "как меня зовут")
	if !strings.Contains(stderr, "забыта") {
		t.Errorf("-forget промолчал:\n%s", stderr)
	}
	sent := p.request(t, 1)
	if len(sent) != 2 {
		t.Fatalf("после -forget отправлено %d сообщений, ожидалось 2: %s", len(sent), contentsOf(sent))
	}
}

// A broken history file must stop the run rather than quietly start a new
// conversation on top of one that is still on disk.
func TestABrokenHistoryStopsTheRun(t *testing.T) {
	p := &provider{}
	srv := p.start(t)
	bin := build(t)
	work := t.TempDir()
	sessions := filepath.Join(work, "sessions")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatalf("подготовка каталога: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessions, "e2e.json"), []byte(`{"version":1,"turns":1,`), 0o600); err != nil {
		t.Fatalf("подготовка файла: %v", err)
	}

	cmd := exec.Command(bin, "-store-dir", sessions, "-session", "e2e", "вопрос")
	cmd.Dir = work
	cmd.Env = append(os.Environ(),
		"DEEPSEEK_API_URL="+srv.URL,
		"DEEPSEEK_API_KEY=e2e-фальшивый-ключ",
		"DEEPSEEK_MODEL=deepseek-v4-flash",
	)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("на битой истории запуск завершился успешно:\n%s", out)
	}
	if !strings.Contains(string(out), "повреждена") {
		t.Errorf("причина не названа:\n%s", out)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.requests) != 0 {
		t.Errorf("на битой истории агент всё равно сходил в модель %d раз", len(p.requests))
	}
}

// converse with /reset in the middle, on the real binary. The unit test covers the
// store's bookkeeping; this covers the path a person actually takes on camera — the
// dialogue mode, the /reset the lesson recommends, and then a genuine restart.
//
// It exists because the conflict check that stopped two processes from overwriting
// each other first mistook the agent's own /reset for someone else's write, and
// everything typed afterwards stopped reaching disk. Nothing in the suite noticed,
// because nothing ran a turn after a reset.
func TestResetInTheReplDoesNotStopTheAgentFromSaving(t *testing.T) {
	p := &provider{answers: []string{"до сброса запомнил", "после сброса запомнил", "и это тоже"}}
	srv := p.start(t)
	bin := build(t)
	work := t.TempDir()
	sessions := filepath.Join(work, "sessions")

	cmd := exec.Command(bin, "-store-dir", sessions, "-session", "repl")
	cmd.Dir = work
	cmd.Env = append(os.Environ(),
		"DEEPSEEK_API_URL="+srv.URL,
		"DEEPSEEK_API_KEY=e2e-фальшивый-ключ",
		"DEEPSEEK_MODEL=deepseek-v4-flash",
	)
	cmd.Stdin = strings.NewReader("первый вопрос\n/reset\nвторой вопрос\n/exit\n")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("диалог: %v\n%s", err, out)
	}
	if strings.Contains(string(out), "не сохранён") {
		t.Fatalf("после /reset агент перестал сохранять:\n%s", out)
	}

	// A new process must see the turn made after the reset, and only that one.
	stdout, stderr := run(t, bin, srv.URL, work,
		"-store-dir", sessions, "-session", "repl", "что я спрашивал")
	if !strings.Contains(stderr, "загружено ходов: 1") {
		t.Errorf("после перезапуска поднялось не то, что писалось после сброса:\n%s", stderr)
	}
	sent := p.request(t, 2)
	if len(sent) != 4 {
		t.Fatalf("отправлено %d сообщений, ожидалось 4: %s", len(sent), contentsOf(sent))
	}
	if !strings.Contains(sent[1].Content, "второй вопрос") {
		t.Errorf("в историю попал не тот ход: %s", contentsOf(sent))
	}
	_ = stdout
}

// Two real processes on one session file — the scenario both review waves reproduced
// in-process, run here through the actual binaries. The unit test proves the store
// refuses the overwrite; this proves the CLI is wired to that store and reports it.
//
// It is deterministic rather than racy: both processes are started and both have
// loaded the same (empty) history before either is given a question. Only then is the
// first one asked, and only after its answer has been read is the second one asked.
// Nothing depends on which process is scheduled first.
func TestTwoRealProcessesOnOneSessionDoNotSilentlyOverwrite(t *testing.T) {
	p := &provider{answers: []string{"первый записал", "второй записал"}}
	srv := p.start(t)
	bin := build(t)
	work := t.TempDir()
	sessions := filepath.Join(work, "sessions")

	start := func(name string) (*exec.Cmd, io.WriteCloser, *bufio.Scanner) {
		cmd := exec.Command(bin, "-store-dir", sessions, "-session", "общая")
		cmd.Dir = work
		cmd.Env = append(os.Environ(),
			"DEEPSEEK_API_URL="+srv.URL,
			"DEEPSEEK_API_KEY=e2e-фальшивый-ключ",
			"DEEPSEEK_MODEL=deepseek-v4-flash",
		)
		in, err := cmd.StdinPipe()
		if err != nil {
			t.Fatalf("%s: stdin: %v", name, err)
		}
		out, err := cmd.StderrPipe()
		if err != nil {
			t.Fatalf("%s: stderr: %v", name, err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatalf("%s: запуск: %v", name, err)
		}
		sc := bufio.NewScanner(out)
		// Both processes read their history at startup; waiting for the line that
		// says so is what makes the order below independent of scheduling.
		for sc.Scan() {
			if strings.Contains(sc.Text(), "готов") {
				return cmd, in, sc
			}
		}
		t.Fatalf("%s: процесс не дошёл до готовности", name)
		return nil, nil, nil
	}

	// Both alive, both having loaded the same empty history.
	firstCmd, firstIn, firstOut := start("первый")
	secondCmd, secondIn, secondOut := start("второй")
	t.Cleanup(func() {
		firstIn.Close()
		secondIn.Close()
		firstCmd.Wait()
		secondCmd.Wait()
	})

	waitForSpend := func(name string, sc *bufio.Scanner) string {
		for sc.Scan() {
			line := sc.Text()
			if strings.Contains(line, "[ход") || strings.Contains(line, "ВНИМАНИЕ") {
				return line
			}
		}
		t.Fatalf("%s: не дождались итога хода", name)
		return ""
	}

	if _, err := io.WriteString(firstIn, "первый пишет\n"); err != nil {
		t.Fatalf("первый: %v", err)
	}
	if line := waitForSpend("первый", firstOut); strings.Contains(line, "ВНИМАНИЕ") {
		t.Fatalf("первый процесс не смог записать свой ход: %s", line)
	}

	if _, err := io.WriteString(secondIn, "второй пишет поверх\n"); err != nil {
		t.Fatalf("второй: %v", err)
	}
	line := waitForSpend("второй", secondOut)
	if !strings.Contains(line, "ВНИМАНИЕ") {
		t.Fatalf("второй процесс молча затёр чужой ход: %s", line)
	}

	// And the first process's turn is still the one on disk.
	raw, err := os.ReadFile(filepath.Join(sessions, "общая.json"))
	if err != nil {
		t.Fatalf("чтение истории: %v", err)
	}
	if !strings.Contains(string(raw), "первый пишет") {
		t.Errorf("ход первого процесса потерян:\n%s", raw)
	}
	if strings.Contains(string(raw), "второй пишет поверх") {
		t.Errorf("ход второго процесса всё-таки затёр историю:\n%s", raw)
	}
}

// Day 8: a measurement run writes synthetic turns into whatever conversation it is
// pointed at, and overwrites that conversation's system prompt with its own. Pointed
// at a real one, it destroys it — atomically, with no copy left. This was not a
// theory: the guard below was added after a probe launched against a session that
// already held fourteen turns spliced its own into them.
func TestATokenProbeRefusesToWriteIntoAConversationThatAlreadyExists(t *testing.T) {
	p := &provider{answers: []string{"запомнил"}}
	srv := p.start(t)
	bin := build(t)
	work := t.TempDir()
	sessions := filepath.Join(work, "sessions")

	run(t, bin, srv.URL, work, "-store-dir", sessions, "-session", "живая", "меня зовут Михаил")
	before := p.calls()

	cmd := exec.Command(bin, "-store-dir", sessions, "-session", "живая", "-token-probe", "growth", "-probe-turns", "3")
	cmd.Dir = work
	cmd.Env = append(os.Environ(),
		"DEEPSEEK_API_URL="+srv.URL,
		"DEEPSEEK_API_KEY=e2e-фальшивый-ключ",
		"DEEPSEEK_MODEL=deepseek-v4-flash",
	)
	var out, errb strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()

	if err == nil {
		t.Fatalf("замер запустился поверх существующей беседы:\nstdout: %s\nstderr: %s", out.String(), errb.String())
	}
	if !strings.Contains(errb.String(), "уже содержит ходов") {
		t.Errorf("отказ не назвал причину:\n%s", errb.String())
	}
	if got := p.calls(); got != before {
		t.Errorf("модель вызвана %d раз после отказа (было %d) — отказ обязан быть бесплатным", got, before)
	}
}
