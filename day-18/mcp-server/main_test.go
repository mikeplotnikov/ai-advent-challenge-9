package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/mcpclient"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/ratesmcp"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/watch"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func fakeCBR(t *testing.T, status int) *httptest.Server {
	t.Helper()
	fixture, err := ratesmcp.Fixtures.ReadFile("fixtures/2026-09-01.xml")
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("локальный HTTP-сервер не запущен: %v", err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if status != http.StatusOK {
			http.Error(w, "broken", status)
			return
		}
		_, _ = w.Write(fixture)
	}))
	server.Listener = listener
	server.Start()
	return server
}

func buildServer(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "server")
	cmd := exec.Command("go", "build", "-o", binary, ".")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %v: %s", err, output)
	}
	return binary
}

func seedDue(t *testing.T, path string) {
	t.Helper()
	store := watch.NewStore(path)
	at := time.Now().UTC().Add(-2 * time.Hour)
	poll := watch.Poll{At: at.Format(time.RFC3339), OK: true, RatesDate: "2026-09-01", Rates: map[string]float64{"USD": 80}}
	if _, err := store.Create([]string{"USD"}, 60, at, poll); err != nil {
		t.Fatal(err)
	}
}

func command(binary, store, url string, args ...string) *exec.Cmd {
	base := []string{"-store", store}
	base = append(base, args...)
	cmd := exec.Command(binary, base...)
	cmd.Env = append(os.Environ(), "CBR_URL="+url)
	return cmd
}

func TestOnceOutcomes(t *testing.T) {
	binary := buildServer(t)
	okServer := fakeCBR(t, http.StatusOK)
	defer okServer.Close()
	t.Run("due and none due", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "store.json")
		seedDue(t, path)
		cmd := command(binary, path, okServer.URL, "-once")
		output, err := cmd.CombinedOutput()
		if err != nil || !bytes.Contains(output, []byte("[планировщик")) {
			t.Fatalf("err=%v output=%s", err, output)
		}
		cmd = command(binary, path, okServer.URL, "-once")
		output, err = cmd.CombinedOutput()
		if err != nil || !bytes.Contains(output, []byte("просроченных наблюдений нет")) {
			t.Fatalf("err=%v output=%s", err, output)
		}
	})
	t.Run("upstream failure is data", func(t *testing.T) {
		bad := fakeCBR(t, http.StatusInternalServerError)
		defer bad.Close()
		path := filepath.Join(t.TempDir(), "store.json")
		seedDue(t, path)
		cmd := command(binary, path, bad.URL, "-once")
		output, err := cmd.CombinedOutput()
		if err != nil || !bytes.Contains(output, []byte("ошибка")) {
			t.Fatalf("err=%v output=%s", err, output)
		}
		state, readErr := watch.NewStore(path).Read()
		if readErr != nil || len(state.Watches[0].Polls) != 2 || state.Watches[0].Polls[1].OK {
			t.Fatalf("state=%+v err=%v", state, readErr)
		}
	})
	t.Run("corrupt", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "store.json")
		raw := []byte("broken")
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		cmd := command(binary, path, okServer.URL, "-once")
		output, err := cmd.CombinedOutput()
		after, _ := os.ReadFile(path)
		if err == nil || !bytes.Equal(raw, after) || !bytes.Contains(output, []byte("файл не изменён")) {
			t.Fatalf("err=%v output=%s after=%q", err, output, after)
		}
	})
}

func TestBackgroundSchedulerAppendsWithoutToolCall(t *testing.T) {
	binary := buildServer(t)
	cbr := fakeCBR(t, http.StatusOK)
	defer cbr.Close()
	path := filepath.Join(t.TempDir(), "store.json")
	seedDue(t, path)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	cmd := command(binary, path, cbr.URL, "-check-every", "1s")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		state, readErr := watch.NewStore(path).Read()
		if readErr == nil && len(state.Watches[0].Polls) >= 2 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("scheduler did not append: %s", stderr.String())
		case <-time.After(20 * time.Millisecond):
		}
	}
	_ = stdin.Close()
	_ = cmd.Wait()
	if !strings.Contains(stderr.String(), "[планировщик") {
		t.Fatalf("stderr=%s", stderr.String())
	}
}

func TestThreeProcessesPreserveAllWatchesAndOnePollPerSlot(t *testing.T) {
	binary := buildServer(t)
	cbr := fakeCBR(t, http.StatusOK)
	defer cbr.Close()
	path := filepath.Join(t.TempDir(), "store.json")
	seedDue(t, path)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, binary, "-store", path, "-check-every", "1s")
	child.Env = append(os.Environ(), "CBR_URL="+cbr.URL)
	transport := &mcp.CommandTransport{Command: child}
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	one, two := command(binary, path, cbr.URL, "-once"), command(binary, path, cbr.URL, "-once")
	if err := one.Start(); err != nil {
		t.Fatal(err)
	}
	if err := two.Start(); err != nil {
		t.Fatal(err)
	}
	codes := []string{"USD", "EUR", "CNY", "GBP"}
	var wg sync.WaitGroup
	errs := make(chan error, len(codes))
	for _, code := range codes {
		code := code
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "create_watch", Arguments: map[string]any{"codes": []any{code}, "every_minutes": 60}})
			if err != nil {
				errs <- err
				return
			}
			if result.IsError {
				errs <- &toolCallError{text: mcpclient.ToolText(result)}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if err := one.Wait(); err != nil {
		t.Error(err)
	}
	if err := two.Wait(); err != nil {
		t.Error(err)
	}
	state, err := watch.NewStore(path).Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Watches) != 5 {
		raw, _ := json.Marshal(state)
		t.Fatalf("watches=%d state=%s", len(state.Watches), raw)
	}
	ids := map[string]bool{}
	seenCodes := map[string]bool{}
	for _, current := range state.Watches {
		if ids[current.ID] {
			t.Errorf("duplicate id %s", current.ID)
		}
		ids[current.ID] = true
		if len(current.Codes) != 1 {
			t.Errorf("%s codes=%v", current.ID, current.Codes)
		} else {
			seenCodes[current.Codes[0]] = true
		}
		want := 1
		if current.ID == "w1" {
			want = 2
		}
		if len(current.Polls) != want {
			t.Errorf("%s polls=%d want=%d", current.ID, len(current.Polls), want)
		}
	}
	for _, code := range []string{"USD", "EUR", "CNY", "GBP"} {
		if !seenCodes[code] {
			t.Errorf("missing code %s", code)
		}
	}
}

func TestGuestCLIFlagEnforcesStopBoundary(t *testing.T) {
	binary := buildServer(t)
	path := filepath.Join(t.TempDir(), "store.json")
	store := watch.NewStore(path)
	at := time.Now().UTC().Add(time.Hour)
	poll := watch.Poll{At: at.Format(time.RFC3339), OK: true, RatesDate: "2026-09-24", Rates: map[string]float64{"USD": 80}}
	created, err := store.Create([]string{"USD"}, 1440, at, poll)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "-store", path, "-guest")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatalf("connect: %v stderr=%s", err, stderr.String())
	}
	defer session.Close()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "stop_watch", Arguments: map[string]any{"watch_id": created.ID}})
	if err != nil || !result.IsError || !strings.Contains(mcpclient.ToolText(result), "гость не останавливает наблюдения") {
		t.Fatalf("result=%+v err=%v stderr=%s", result, err, stderr.String())
	}
	state, err := store.Read()
	if err != nil || len(state.Watches) != 1 || state.Watches[0].Status != "active" {
		t.Fatalf("state=%+v err=%v", state, err)
	}
}

type toolCallError struct{ text string }

func (e *toolCallError) Error() string { return e.text }
