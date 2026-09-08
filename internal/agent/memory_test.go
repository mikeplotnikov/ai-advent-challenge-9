package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
)

func storeIn(t *testing.T) *FileStore {
	t.Helper()
	return NewFileStore(filepath.Join(t.TempDir(), "session.json"))
}

// The day's requirement, stated as the task states it: "при перезапуске агента
// загружайте историю обратно; продолжайте диалог так, как будто агент не выключался".
//
// A restarted process is a new Agent value over the same store, so the check is that
// the SECOND agent sends the FIRST agent's exchanges to the model. Asserting on what
// went over the wire, not on what the file contains: a file that is written and never
// read back into the request would pass a "did we save it" test and fail the task.
func TestASecondAgentContinuesTheFirstConversation(t *testing.T) {
	store := storeIn(t)
	cfg := Config{SystemPrompt: "ты ассистент", Model: "deepseek-v4-flash", Store: store}

	first := &fakeCaller{answers: []llm.Answer{{Content: "приятно познакомиться, Михаил", Model: "deepseek-v4-flash"}}}
	a1 := newAgent(t, cfg, first)
	if _, err := a1.Ask(context.Background(), "меня зовут Михаил"); err != nil {
		t.Fatalf("первый запуск: %v", err)
	}

	// The process ends here. Nothing of a1 is carried over.
	second := &fakeCaller{}
	a2 := newAgent(t, cfg, second)

	if got := a2.Turns(); got != 1 {
		t.Errorf("после перезапуска ходов %d, ожидался 1", got)
	}
	if got := a2.Restored().Turns; got != 1 {
		t.Errorf("Restored().Turns = %d, ожидался 1", got)
	}
	if _, err := a2.Ask(context.Background(), "как меня зовут"); err != nil {
		t.Fatalf("второй запуск: %v", err)
	}

	sent := second.sent[0]
	if want := "system,user,assistant,user"; roles(sent) != want {
		t.Fatalf("после перезапуска отправлены роли %s, ожидались %s", roles(sent), want)
	}
	if !strings.Contains(sent[1].Content, "Михаил") {
		t.Errorf("вопрос первого запуска не уехал в модель: %q", sent[1].Content)
	}
	if !strings.Contains(sent[2].Content, "Михаил") {
		t.Errorf("ответ первого запуска не уехал в модель: %q", sent[2].Content)
	}
}

// The negative control for the test above. Without a store the same two runs must
// leave the second agent knowing nothing — otherwise the first test is passing for
// some reason other than the store, and neither test means anything.
func TestWithoutAStoreASecondAgentRemembersNothing(t *testing.T) {
	cfg := Config{SystemPrompt: "ты ассистент", Model: "deepseek-v4-flash"}

	a1 := newAgent(t, cfg, &fakeCaller{})
	if _, err := a1.Ask(context.Background(), "меня зовут Михаил"); err != nil {
		t.Fatalf("первый запуск: %v", err)
	}

	second := &fakeCaller{}
	a2 := newAgent(t, cfg, second)
	if got := a2.Turns(); got != 0 {
		t.Fatalf("без хранилища агент помнит %d ходов — контроль сломан", got)
	}
	if _, err := a2.Ask(context.Background(), "как меня зовут"); err != nil {
		t.Fatalf("второй запуск: %v", err)
	}
	sent := second.sent[0]
	if want := "system,user"; roles(sent) != want {
		t.Fatalf("без хранилища отправлены роли %s, ожидались %s", roles(sent), want)
	}
	for _, m := range sent {
		if strings.Contains(m.Content, "Михаил") {
			t.Fatalf("без хранилища в запрос просочилась прошлая беседа: %q", m.Content)
		}
	}
}

// A turn joins the file only when it joined the conversation. Day 6 established that
// a rejected answer must not poison the next request's context; if it were written to
// disk anyway, the next RUN would be poisoned instead, which is worse — it survives.
func TestARejectedTurnIsNotStored(t *testing.T) {
	store := storeIn(t)
	cfg := Config{
		SystemPrompt: "ты ассистент",
		Store:        store,
		Validate:     func(string) error { return errors.New("не по схеме") },
	}
	a := newAgent(t, cfg, &fakeCaller{})
	if _, err := a.Ask(context.Background(), "вопрос"); err == nil {
		t.Fatal("Ask: ожидалась ошибка проверки ответа")
	}

	snap, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(snap.Messages) != 0 || snap.Turns != 0 {
		t.Fatalf("отклонённый ход попал в файл: ходов %d, сообщений %d", snap.Turns, len(snap.Messages))
	}
}

// An empty answer is a failed call that was still billed (day 5). It must not be
// stored either — an assistant message with no content would be sent back to the
// model on the next run as if it had said nothing on purpose.
func TestAnEmptyAnswerIsNotStored(t *testing.T) {
	store := storeIn(t)
	f := &fakeCaller{answers: []llm.Answer{{Content: "   ", Model: "deepseek-v4-flash"}}}
	a := newAgent(t, Config{Store: store}, f)

	if _, err := a.Ask(context.Background(), "вопрос"); !errors.Is(err, ErrEmptyAnswer) {
		t.Fatalf("Ask: ожидалась ErrEmptyAnswer, получено %v", err)
	}
	snap, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(snap.Messages) != 0 {
		t.Fatalf("пустой ответ записан в историю: %+v", snap.Messages)
	}
}

// Reset is "conversation recreation" from the lesson. With a store it has to reach
// the disk: a conversation the owner told the agent to forget coming back on the next
// start is the one failure mode day 7 adds that day 6 could not have.
func TestResetClearsTheStoredConversation(t *testing.T) {
	store := storeIn(t)
	cfg := Config{Store: store}
	a := newAgent(t, cfg, &fakeCaller{})
	if _, err := a.Ask(context.Background(), "вопрос"); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if err := a.Reset(); err != nil {
		t.Fatalf("Reset: %v", err)
	}

	if _, err := os.Stat(store.Path()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("после Reset файл истории всё ещё на месте: %v", err)
	}
	next := newAgent(t, cfg, &fakeCaller{})
	if next.Turns() != 0 {
		t.Errorf("после Reset новый агент поднял %d ходов", next.Turns())
	}
}

// The window applies to what was loaded, not only to what this run adds. Otherwise
// -max-turns would cap the visible conversation while an unbounded history from the
// file kept going to the model — and the token bill would not match the setting.
func TestTheWindowAppliesToLoadedHistory(t *testing.T) {
	store := storeIn(t)
	full := Config{SystemPrompt: "ты ассистент", Store: store}
	a := newAgent(t, full, &fakeCaller{})
	for _, q := range []string{"первый", "второй", "третий"} {
		if _, err := a.Ask(context.Background(), q); err != nil {
			t.Fatalf("Ask(%q): %v", q, err)
		}
	}

	windowed := full
	windowed.MaxTurns = 1
	f := &fakeCaller{}
	b := newAgent(t, windowed, f)
	if b.Turns() != 3 {
		t.Fatalf("после загрузки Turns() = %d, ожидалось 3: окно режет контекст, а не счётчик", b.Turns())
	}
	if _, err := b.Ask(context.Background(), "четвёртый"); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	sent := f.sent[0]
	if want := "system,user,assistant,user"; roles(sent) != want {
		t.Fatalf("с окном 1 отправлены роли %s, ожидались %s", roles(sent), want)
	}
	if sent[1].Content != "третий" {
		t.Errorf("в окно попал %q, ожидался последний обмен «третий»", sent[1].Content)
	}
	// The count of what happened is not the count of what is being sent: three
	// exchanges were loaded and a fourth has now been made, while the window keeps
	// exactly one of them in the request.
	if b.Turns() != 4 {
		t.Errorf("Turns() = %d, ожидалось 4", b.Turns())
	}
}

// A broken file is refused loudly. Starting empty would erase a conversation that is
// still on disk, at the exact moment someone restarts the agent expecting it back.
func TestABrokenHistoryIsRefusedRatherThanIgnored(t *testing.T) {
	cases := map[string]string{
		"обрезанный JSON":     `{"version":1,"turns":1,"messages":[{"role":"user",`,
		"пустой файл":         "   \n",
		"чужая роль в стеке":  `{"version":1,"turns":1,"messages":[{"role":"system","content":"я системный промпт"},{"role":"assistant","content":"ответ"}]}`,
		"неполный обмен":      `{"version":1,"turns":1,"messages":[{"role":"user","content":"вопрос"}]}`,
		"обмен задом наперёд": `{"version":1,"turns":1,"messages":[{"role":"assistant","content":"ответ"},{"role":"user","content":"вопрос"}]}`,
		"пустое сообщение":    `{"version":1,"turns":1,"messages":[{"role":"user","content":"  "},{"role":"assistant","content":"ответ"}]}`,
		"версия из будущего":  `{"version":99,"turns":0,"messages":[]}`,
		"версии нет вовсе":    `{"turns":1,"messages":[{"role":"user","content":"в"},{"role":"assistant","content":"о"}]}`,
		"ходов меньше обменов": `{"version":1,"turns":0,"messages":[{"role":"user","content":"в"},` +
			`{"role":"assistant","content":"о"}]}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "session.json")
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatalf("подготовка файла: %v", err)
			}
			store := NewFileStore(path)
			if _, err := store.Load(); err == nil {
				t.Fatal("Load: битая история принята молча")
			}
			if _, err := New(&fakeCaller{}, Config{Store: store}); err == nil {
				t.Fatal("New: агент поднялся на битой истории")
			}
			if _, err := os.Stat(path); err != nil {
				t.Errorf("битый файл не сохранён для разбора: %v", err)
			}
		})
	}
}

// The ordinary first run: no file yet, no error, no history.
func TestAMissingHistoryIsAnEmptyConversation(t *testing.T) {
	store := storeIn(t)
	snap, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if snap.Turns != 0 || len(snap.Messages) != 0 {
		t.Fatalf("пустое хранилище вернуло %+v", snap)
	}
	a := newAgent(t, Config{Store: store}, &fakeCaller{})
	if a.Turns() != 0 || len(a.Restored().Warnings) != 0 {
		t.Fatalf("на пустом хранилище агент поднялся с %d ходов и предупреждениями %v",
			a.Turns(), a.Restored().Warnings)
	}
}

// A conversation recorded under one role and replayed under another is a different
// conversation. It is not refused — the exchanges are still real — but it is named.
func TestAChangedSystemPromptIsReportedNotSwallowed(t *testing.T) {
	store := storeIn(t)
	a := newAgent(t, Config{SystemPrompt: "ты пират", Model: "deepseek-v4-flash", Store: store}, &fakeCaller{})
	if _, err := a.Ask(context.Background(), "вопрос"); err != nil {
		t.Fatalf("Ask: %v", err)
	}

	b := newAgent(t, Config{SystemPrompt: "ты юрист", Model: "deepseek-v4-pro", Store: store}, &fakeCaller{})
	warnings := strings.Join(b.Restored().Warnings, " | ")
	if !strings.Contains(warnings, "промпт") {
		t.Errorf("смена системного промпта не названа: %q", warnings)
	}
	if !strings.Contains(warnings, "deepseek-v4-pro") {
		t.Errorf("смена модели не названа: %q", warnings)
	}
	if b.Turns() != 1 {
		t.Errorf("история выброшена из-за предупреждения: ходов %d", b.Turns())
	}
}

// A turn that was answered but could not be written down must say so. Silence here is
// the worst failure the day has: the agent looks like it remembers, right up to the
// restart in front of the camera.
func TestAFailedWriteIsReportedAndKeepsThePreviousHistory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("права на каталог проверяются на unix")
	}
	if os.Geteuid() == 0 {
		t.Skip("root игнорирует права каталога — контроль недействителен")
	}
	dir := t.TempDir()
	store := NewFileStore(filepath.Join(dir, "session.json"))
	cfg := Config{Store: store}
	a := newAgent(t, cfg, &fakeCaller{})
	if _, err := a.Ask(context.Background(), "первый"); err != nil {
		t.Fatalf("первый ход: %v", err)
	}

	// The directory becomes unwritable: the rename cannot land.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })

	reply, err := a.Ask(context.Background(), "второй")
	if !errors.Is(err, ErrNotSaved) {
		t.Fatalf("Ask: ожидалась ErrNotSaved, получено %v", err)
	}
	if reply.Text == "" {
		t.Error("ответ потерян вместе с ошибкой записи — вызов был оплачен, текст обязан вернуться")
	}
	if reply.Turn != 2 {
		t.Errorf("Reply.Turn = %d, ожидался 2", reply.Turn)
	}

	os.Chmod(dir, 0o700)
	snap, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if snap.Turns != 1 || len(snap.Messages) != 2 {
		t.Fatalf("прошлая история повреждена неудачной записью: ходов %d, сообщений %d",
			snap.Turns, len(snap.Messages))
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("после неудачной записи в каталоге %d файлов, ожидался один: %v", len(entries), entries)
	}
}

// The write is atomic by rename, so a reader never sees half a file and a crash never
// leaves one. What is checked here is the observable part: no temporary files left
// behind, and the file readable straight after every turn.
func TestEveryTurnLeavesAReadableFileAndNoLeftovers(t *testing.T) {
	dir := t.TempDir()
	store := NewFileStore(filepath.Join(dir, "session.json"))
	a := newAgent(t, Config{Store: store}, &fakeCaller{})

	for i := 1; i <= 4; i++ {
		if _, err := a.Ask(context.Background(), "вопрос"); err != nil {
			t.Fatalf("ход %d: %v", i, err)
		}
		snap, err := store.Load()
		if err != nil {
			t.Fatalf("после хода %d: %v", i, err)
		}
		if snap.Turns != i || len(snap.Messages) != i*2 {
			t.Fatalf("после хода %d записано ходов %d, сообщений %d", i, snap.Turns, len(snap.Messages))
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("чтение каталога: %v", err)
		}
		if len(entries) != 1 {
			t.Fatalf("после хода %d в каталоге %d файлов — остался временный", i, len(entries))
		}
	}
}

// The file is the artifact shown on camera and read back by the next run, so its
// shape is fixed here rather than left to whatever the struct happens to marshal to.
func TestTheStoredFileIsPlainReadableJSON(t *testing.T) {
	store := storeIn(t)
	cfg := Config{Name: "агент", SystemPrompt: "ты ассистент", Model: "deepseek-v4-flash", Store: store}
	a := newAgent(t, cfg, &fakeCaller{})
	if _, err := a.Ask(context.Background(), "меня зовут Михаил"); err != nil {
		t.Fatalf("Ask: %v", err)
	}

	raw, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatalf("чтение файла: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("файл не разбирается как JSON: %v", err)
	}
	for _, key := range []string{"version", "agent", "model", "system", "turns", "updated", "messages"} {
		if _, ok := got[key]; !ok {
			t.Errorf("в файле нет поля %q", key)
		}
	}
	if !strings.Contains(string(raw), "\n  ") {
		t.Error("файл записан одной строкой — его смысл в том, что его можно прочитать глазами")
	}
	if got["system"] != "ты ассистент" {
		t.Errorf("system = %v", got["system"])
	}

	info, err := os.Stat(store.Path())
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("права на историю %v, ожидались 0600: это личная переписка владельца", info.Mode().Perm())
	}
}

// A session name arrives from a command line flag. It must not be able to name a file
// outside the sessions directory.
func TestSessionNamesCannotEscapeTheDirectory(t *testing.T) {
	cases := []string{"../../.env", "/etc/passwd", "..", ".", "", "  ", "a/b", `..\..\win`}
	for _, name := range cases {
		got := SessionPath("sessions", name)
		if dir := filepath.Dir(got); dir != "sessions" {
			t.Errorf("SessionPath(%q) = %q — вышло за пределы каталога", name, got)
		}
		if strings.Contains(filepath.Base(got), "..") {
			t.Errorf("SessionPath(%q) = %q", name, got)
		}
	}
	// Cyrillic names survive intact. They used not to: every one of them folded to
	// the same fallback, so two different Russian session names — and the default
	// session with them — were one file.
	if got, want := SessionPath("sessions", "рабочая"), filepath.Join("sessions", "рабочая.json"); got != want {
		t.Errorf("SessionPath с кириллицей = %q, ожидалось %q", got, want)
	}
	// Names that cannot survive at all must at least stay different from each other
	// and from the default session.
	seen := map[string]string{}
	for _, name := range []string{"..", ".", "///", "***", "%%%"} {
		got := SessionPath("sessions", name)
		if prev, dup := seen[got]; dup {
			t.Errorf("имена %q и %q дали один файл %q", prev, name, got)
		}
		seen[got] = name
		if got == SessionPath("sessions", "") {
			t.Errorf("имя %q схлопнулось в беседу по умолчанию", name)
		}
	}
	if got, want := SessionPath("sessions", "work-2_a"), filepath.Join("sessions", "work-2_a.json"); got != want {
		t.Errorf("SessionPath = %q, ожидалось %q", got, want)
	}
}

// Two sessions are two conversations. Without this the -session flag would be
// decoration and the demo's second half ("а вот другая беседа, она вас не знает")
// would be a coincidence.
func TestTwoSessionsDoNotSeeEachOther(t *testing.T) {
	dir := t.TempDir()
	cfg := func(name string) Config {
		return Config{SystemPrompt: "ты ассистент", Store: NewFileStore(SessionPath(dir, name))}
	}
	a := newAgent(t, cfg("work"), &fakeCaller{})
	if _, err := a.Ask(context.Background(), "меня зовут Михаил"); err != nil {
		t.Fatalf("Ask: %v", err)
	}

	f := &fakeCaller{}
	b := newAgent(t, cfg("home"), f)
	if b.Turns() != 0 {
		t.Fatalf("вторая беседа подняла %d ходов чужой истории", b.Turns())
	}
	if _, err := b.Ask(context.Background(), "как меня зовут"); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	for _, m := range f.sent[0] {
		if strings.Contains(m.Content, "Михаил") {
			t.Fatalf("история перетекла между беседами: %q", m.Content)
		}
	}
}

// The snapshot carries a time so the interface can say when the conversation was last
// touched. A zero time printed as "01.01 00:00" would look like a bug in the clock.
func TestTheSnapshotRecordsWhenItWasWritten(t *testing.T) {
	store := storeIn(t)
	before := time.Now().Add(-time.Second)
	a := newAgent(t, Config{Store: store}, &fakeCaller{})
	if _, err := a.Ask(context.Background(), "вопрос"); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	snap, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if snap.Updated.Before(before) {
		t.Errorf("Updated = %v, записано до начала теста", snap.Updated)
	}
	b := newAgent(t, Config{Store: store}, &fakeCaller{})
	if b.Restored().Updated.IsZero() {
		t.Error("Restored().Updated пуст — интерфейсу нечего показать")
	}
}
