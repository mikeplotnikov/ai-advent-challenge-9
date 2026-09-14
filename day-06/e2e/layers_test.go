package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func repl(t *testing.T, bin, url, workdir, input string, args ...string) string {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Dir = workdir
	cmd.Env = append(os.Environ(),
		"DEEPSEEK_API_URL="+url,
		"DEEPSEEK_API_KEY=e2e-фальшивый-ключ",
		"DEEPSEEK_MODEL=deepseek-v4-flash",
	)
	cmd.Stdin = strings.NewReader(input)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("диалог %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// Day 11 through the real binary: explicit writes in one process, a new session in a
// second process, and /reset — with the provider's received requests as the evidence.
func TestLayersSurviveANewSessionAndResetThroughTheCLI(t *testing.T) {
	p := &provider{}
	srv := p.start(t)
	bin := build(t)
	work := t.TempDir()
	mem := filepath.Join(work, "mem")
	base := []string{"-layers", "-memory-dir", mem, "-user", "михаил"}

	out := repl(t, bin, srv.URL, work,
		"тикет BUG-7781\n"+
			"/task new T-SYNC\n"+
			"/remember task export_code = EXP-5531\n"+
			"/remember decision storage = PostgreSQL 16, DEC-0412\n"+
			"/remember profile answer_format = Начинай с ИТОГ:\n"+
			"/memory\n/exit\n",
		append(base, "-session", "s1")...)
	if !strings.Contains(out, "сохранено: task → рабочий слой") || !strings.Contains(out, "сохранено: decision → долговременный слой") {
		t.Fatalf("CLI не назвал слой записи:\n%s", out)
	}
	if !strings.Contains(out, "DEC-0412") || !strings.Contains(out, "EXP-5531") {
		t.Fatalf("/memory не показал записи:\n%s", out)
	}

	longTerm, err := os.ReadFile(filepath.Join(mem, "михаил", "long-term.json"))
	if err != nil {
		t.Fatal(err)
	}
	task, err := os.ReadFile(filepath.Join(mem, "михаил", "tasks", "T-SYNC.json"))
	if err != nil {
		t.Fatal(err)
	}
	session, err := os.ReadFile(filepath.Join(mem, "михаил", "sessions", "s1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(longTerm), "DEC-0412") || strings.Contains(string(longTerm), "EXP-5531") || strings.Contains(string(longTerm), "BUG-7781") {
		t.Fatal("долговременный файл содержит не своё")
	}
	if !strings.Contains(string(task), "EXP-5531") || strings.Contains(string(task), "DEC-0412") {
		t.Fatal("файл задачи содержит не своё")
	}
	if !strings.Contains(string(session), "BUG-7781") || strings.Contains(string(session), "DEC-0412") {
		t.Fatal("краткосрочный файл содержит не своё")
	}

	// A second process, a new session of the same user, with the task given by flag.
	repl(t, bin, srv.URL, work, "какой код выгрузки?\n/reset\nснова вопрос\n/exit\n",
		append(base, "-session", "s2", "-task", "T-SYNC")...)
	first := contentsOf(p.request(t, p.calls()-2))
	if strings.Contains(first, "BUG-7781") {
		t.Fatalf("краткосрочная память сессии s1 утекла в s2: %s", first)
	}
	for _, marker := range []string{"DEC-0412", "ИТОГ:", "EXP-5531"} {
		if !strings.Contains(first, marker) {
			t.Fatalf("новая сессия не отправила %s: %s", marker, first)
		}
	}
	after := contentsOf(p.request(t, p.calls()-1))
	if !strings.Contains(after, "DEC-0412") || !strings.Contains(after, "EXP-5531") || strings.Contains(after, "какой код выгрузки") {
		t.Fatalf("после /reset слои пропали или история осталась: %s", after)
	}
	if got, _ := os.ReadFile(filepath.Join(mem, "михаил", "long-term.json")); string(got) != string(longTerm) {
		t.Fatal("/reset изменил долговременную память")
	}
}

func TestLayersRefuseHistoricalProbesAndStrayFlags(t *testing.T) {
	bin := build(t)
	work := t.TempDir()
	for _, tc := range []struct {
		args []string
		why  string
	}{
		{[]string{"-layers", "-token-probe", "growth"}, "нельзя совмещать с -layers"},
		{[]string{"-layers", "-no-memory", "вопрос"}, "-layers и -no-memory"},
		{[]string{"-user", "михаил", "вопрос"}, "-user действует только вместе с -layers"},
	} {
		cmd := exec.Command(bin, tc.args...)
		cmd.Dir = work
		cmd.Env = append(os.Environ(), "DEEPSEEK_API_URL=http://127.0.0.1:1", "DEEPSEEK_API_KEY=e2e")
		out, err := cmd.CombinedOutput()
		if err == nil || !strings.Contains(string(out), tc.why) {
			t.Fatalf("%v: ожидался отказ «%s», получено (%v):\n%s", tc.args, tc.why, err, out)
		}
	}
}
