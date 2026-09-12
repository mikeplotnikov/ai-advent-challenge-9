package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
)

// Day 7 asks for one thing: "храните историю диалога (messages) в JSON или SQLite;
// при перезапуске агента загружайте историю обратно; продолжайте диалог так, как
// будто агент не выключался."
//
// The subject of "храните" is not spelled out, and the host never answered the
// question when it was put to him on 2026-09-07 (chat 2159: does the agent manage
// storage itself, or is it handed messages from outside?). Two of his other
// statements settle it between them: the task says "добавьте АГЕНТУ сохранение
// контекста", and on day 6 he required that the agent "сама на себя брать
// складывание стека сообщений" (chat 2127). So the agent owns the history — it
// loads it when it is built and writes it after every completed turn.
//
// What the agent does NOT own is the medium. Store is the seam: JSON today because
// a file can be opened on camera and read aloud, SQLite in week 3 when the lesson
// is state management, without the agent noticing either way.

// Message is one turn of the conversation in the agent's own vocabulary. It exists
// so that a Store — including one written outside this package — never has to know
// what an llm.Message is. The transport's types stop at this package's edge, which
// is the property day 6's encapsulation test enforces from the other side.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Roles the stack is allowed to contain. The system prompt is deliberately absent:
// the agent keeps it out of the stack and prepends it at send time, so that changing
// the role never leaves a stale system message buried in the history. A stored file
// containing one would resurrect exactly that bug.
const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
)

// SnapshotVersion is the format of what gets written. It is stored so that a future
// change is detected rather than silently misread: a file from a newer version is
// refused, not parsed on a guess.
//
// Version 2 added Spend, version 3 compressed history, and version 4 the three
// mutually exclusive day-10 context strategies. Older versions are still read.
const SnapshotVersion = 4

// Snapshot is the conversation as it is written down. Messages are the whole of what
// the task asks to store; the rest is what restoring needs in order not to lie.
type Snapshot struct {
	Version int    `json:"version"`
	Agent   string `json:"agent"`
	// Model and System are the fingerprint of the agent that wrote this. Replaying a
	// conversation under a different role is a different conversation, and the caller
	// is told when that happens instead of finding out from the model's answers.
	Model  string `json:"model"`
	System string `json:"system"`
	// Turns is how many exchanges the agent has completed in total. It is not
	// len(Messages)/2: a window (MaxTurns) drops old exchanges from the stack while
	// the count of what happened stays what it was.
	Turns   int       `json:"turns"`
	Updated time.Time `json:"updated"`
	// Spend is what this conversation has cost so far, carried across restarts.
	// Day 8: the running total is a property of the conversation, not of the process
	// that happens to be holding it — a process restarted twice a day would
	// otherwise report the third of the bill it can still see. Absent in version-1
	// files, where its zero value is the truthful answer: nothing was recorded.
	// No omitempty: encoding/json never omits a struct value, so the tag would have
	// promised an omission that does not happen.
	Spend Totals `json:"spend"`
	// Summary holds the semantic replacement for messages no longer present below.
	// It is deliberately not put into Messages: the task asks to keep the newest N
	// messages "as is", and treating a model-written summary as one of them would
	// blur the two layers on disk and after restart.
	Summary            string `json:"summary"`
	CompressedMessages int    `json:"compressedMessages"`
	// SummarySpend is the subset of Spend spent producing summaries. The total still
	// includes it: a comparison that leaves it out reports an imaginary saving.
	SummarySpend Totals    `json:"summarySpend"`
	Messages     []Message `json:"messages"`

	// Day 10 never uses Summary. These fields persist the selected strategy and its
	// explicit state so a restart cannot silently turn facts or branches into a flat
	// full-history conversation.
	Strategy       ContextStrategy      `json:"strategy,omitempty"`
	WindowMessages int                  `json:"windowMessages,omitempty"`
	Facts          map[string]string    `json:"facts,omitempty"`
	FactSpend      Totals               `json:"factSpend"`
	ActiveBranch   string               `json:"activeBranch,omitempty"`
	Branches       map[string][]Message `json:"branches,omitempty"`
	Checkpoints    map[string][]Message `json:"checkpoints,omitempty"`
}

// Store is where a conversation lives between runs. Load on an empty store returns a
// zero Snapshot and no error: "nothing saved yet" is the normal first run, not a
// failure. Anything else — unreadable, malformed, from a future version — is an
// error, because silently starting from scratch would erase a conversation the owner
// still has, and would do it at exactly the moment the day's demo is running.
type Store interface {
	Load() (Snapshot, error)
	Save(Snapshot) error
	Clear() error
}

// ErrNotSaved wraps a store failure on a turn that otherwise succeeded. The reply is
// still returned in full — the model was called and the answer is real — but the
// caller is told that this turn will not survive a restart. Reporting it as success
// would produce the day-7 demo's worst outcome: an agent that looks like it remembers
// until the process is restarted in front of the camera.
var ErrNotSaved = errors.New("ход не сохранён")

// FileStore keeps one conversation in one JSON file.
type FileStore struct {
	path string
	// lastSeen is the Updated stamp of the version this store last read or wrote.
	// It exists because two processes on the same -session are not prevented by
	// anything: each does its own load-modify-save, and without this the later
	// save silently overwrites the earlier one's turn, reporting success to both.
	// A silently dropped turn is the one outcome day 7 must not produce, so the
	// conflict is detected and named instead. Detection, not locking: a lock would
	// be the wrong size for a single-user CLI, and a named error is enough to keep
	// the loss from being invisible.
	lastSeen time.Time
	loaded   bool
}

// ErrChangedElsewhere is returned when the file moved under the store — another
// process wrote the same conversation between this store's last read and this write.
var ErrChangedElsewhere = errors.New("беседа изменена другим процессом")

// NewFileStore points a store at a file. Nothing is read or created until Load.
func NewFileStore(path string) *FileStore { return &FileStore{path: path} }

// Path is where this conversation lives, for an interface that wants to say so.
func (s *FileStore) Path() string { return s.path }

// SessionPath maps a session name to a file inside dir. The name is sanitised rather
// than trusted: it arrives from a command line flag, and a session called "../.env"
// must not be able to name a file outside dir.
func SessionPath(dir, session string) string {
	return filepath.Join(dir, sessionFileName(session)+".json")
}

// sessionFileName keeps letters, digits, dash and underscore — letters in any script,
// because the owner names things in Russian — and replaces everything else, including
// separators and dots, with a dash.
//
// The case that matters is what happens when nothing survives. An earlier version
// kept ASCII only and folded every Cyrillic name to the same fallback: "рабочая" and
// "домашняя" would both have become "default", silently merging two conversations
// and the default one. So a name that sanitises to nothing keeps its identity through
// a hash of the original instead, and the same hash caps a name too long for a
// filesystem to hold.
func sessionFileName(session string) string {
	var b strings.Builder
	for _, r := range session {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r), r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	name := strings.Trim(b.String(), "-")
	switch {
	case strings.TrimSpace(session) == "":
		return "default"
	case name == "":
		return "session-" + shortHash(session)
	case len([]rune(name)) > 60:
		return string([]rune(name)[:60]) + "-" + shortHash(session)
	}
	return name
}

// shortHash identifies a session name that cannot be used as a file name, so that two
// different unusable names do not become one conversation.
func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:4])
}

// Load reads the conversation. A missing file is an empty conversation; a broken one
// is an error naming the file, so that the fix is obvious and the file is still there
// to be fixed.
func (s *FileStore) Load() (Snapshot, error) {
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		s.lastSeen = time.Time{}
		s.loaded = true
		return Snapshot{}, nil
	}
	if err != nil {
		return Snapshot{}, fmt.Errorf("история %s не читается: %w", s.path, err)
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return Snapshot{}, fmt.Errorf("история %s пуста — файл есть, содержимого нет", s.path)
	}

	var snap Snapshot
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&snap); err != nil {
		return Snapshot{}, fmt.Errorf("история %s повреждена: %w", s.path, err)
	}
	if err := validate(snap); err != nil {
		return Snapshot{}, fmt.Errorf("история %s: %w", s.path, err)
	}
	s.lastSeen = snap.Updated
	s.loaded = true
	return snap, nil
}

// validate refuses a file this agent cannot honestly continue from.
func validate(snap Snapshot) error {
	if snap.Version > SnapshotVersion {
		return fmt.Errorf("версия формата %d, а эта сборка понимает только %d", snap.Version, SnapshotVersion)
	}
	if snap.Version < 1 {
		return fmt.Errorf("версия формата %d — файл записан не этим агентом", snap.Version)
	}
	for i, m := range snap.Messages {
		if m.Role != RoleUser && m.Role != RoleAssistant {
			return fmt.Errorf("сообщение %d имеет роль %q — в стеке допустимы только %q и %q",
				i+1, m.Role, RoleUser, RoleAssistant)
		}
		if strings.TrimSpace(m.Content) == "" {
			return fmt.Errorf("сообщение %d пустое", i+1)
		}
		// The stack is a sequence of complete exchanges, user first. A lone user
		// message would be sent to the model as a question it had already ignored.
		want := RoleUser
		if i%2 == 1 {
			want = RoleAssistant
		}
		if m.Role != want {
			return fmt.Errorf("сообщение %d имеет роль %q, ожидалась %q — стек не разбивается на полные обмены",
				i+1, m.Role, want)
		}
	}
	if len(snap.Messages)%2 != 0 {
		return fmt.Errorf("сообщений %d — нечётное число, последний обмен неполон", len(snap.Messages))
	}
	if snap.Turns < len(snap.Messages)/2 {
		return fmt.Errorf("ходов записано %d, а сообщений на %d обменов", snap.Turns, len(snap.Messages)/2)
	}
	if err := validateSpend(snap.Spend); err != nil {
		return err
	}
	if strings.TrimSpace(snap.Summary) == "" && snap.CompressedMessages > 0 {
		return fmt.Errorf("сжато сообщений %d, но резюме пусто", snap.CompressedMessages)
	}
	if snap.CompressedMessages < 0 || snap.CompressedMessages%2 != 0 {
		return fmt.Errorf("сжато сообщений %d — нужны неотрицательное чётное число полных сообщений", snap.CompressedMessages)
	}
	if err := validateSpend(snap.SummarySpend); err != nil {
		return fmt.Errorf("расход суммаризации: %w", err)
	}
	if err := validateSpendSubset(snap.SummarySpend, snap.Spend); err != nil {
		return fmt.Errorf("расход суммаризации: %w", err)
	}
	if err := validateStrategySnapshot(snap); err != nil {
		return err
	}
	return nil
}

// validateSpendSubset makes the summary subtotal auditable after a restart. It may
// equal the whole conversation (a summarizer failed before a normal answer was ever
// recorded), but it can never exceed the requests the conversation says it made.
func validateSpendSubset(summary, total Totals) error {
	for _, f := range []struct {
		name    string
		summary int
		total   int
	}{
		{"вызовов", summary.Calls, total.Calls},
		{"неудачных вызовов", summary.Failed, total.Failed},
		{"входных токенов", summary.PromptTokens, total.PromptTokens},
		{"выходных токенов", summary.CompletionTokens, total.CompletionTokens},
		{"токенов рассуждения", summary.ReasoningTokens, total.ReasoningTokens},
		{"токенов из кэша", summary.CachedTokens, total.CachedTokens},
		{"токенов мимо кэша", summary.MissedTokens, total.MissedTokens},
		{"вызовов по неизвестной цене", summary.Unpriced, total.Unpriced},
	} {
		if f.summary > f.total {
			return fmt.Errorf("%s суммаризации %d больше общего расхода %d", f.name, f.summary, f.total)
		}
	}
	if summary.Cost > total.Cost {
		return fmt.Errorf("цена суммаризации %.6f больше общей цены %.6f", summary.Cost, total.Cost)
	}
	return nil
}

// validateSpend refuses a recorded total that cannot have happened. A negative token
// count or more failures than calls means the file was edited or written by something
// else, and continuing from it would put a fabricated number into every report the
// conversation produces from here on.
func validateSpend(t Totals) error {
	for _, f := range []struct {
		name  string
		value int
	}{
		{"вызовов", t.Calls},
		{"неудачных вызовов", t.Failed},
		{"входных токенов", t.PromptTokens},
		{"выходных токенов", t.CompletionTokens},
		{"токенов рассуждения", t.ReasoningTokens},
		{"токенов из кэша", t.CachedTokens},
		{"токенов мимо кэша", t.MissedTokens},
		{"вызовов по неизвестной цене", t.Unpriced},
	} {
		if f.value < 0 {
			return fmt.Errorf("в записанном расходе %s: %d — отрицательным быть не может", f.name, f.value)
		}
	}
	if t.Failed > t.Calls {
		return fmt.Errorf("в записанном расходе неудачных вызовов %d при %d вызовах всего", t.Failed, t.Calls)
	}
	if t.Cost < 0 {
		return fmt.Errorf("в записанном расходе цена %f — отрицательной быть не может", t.Cost)
	}
	return nil
}

// Save writes the conversation so that a crash cannot leave half of it on disk: a
// temporary file in the same directory, then a rename, which is atomic within a
// filesystem. Writing in place would mean the one moment the process can die — while
// the file is truncated and half-written — destroys the history the day is about.
func (s *FileStore) Save(snap Snapshot) error {
	snap.Version = SnapshotVersion
	if snap.Updated.IsZero() {
		snap.Updated = time.Now()
	}
	if err := s.checkUnchanged(); err != nil {
		return err
	}
	body, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return fmt.Errorf("история %s: %w", s.path, err)
	}
	body = append(body, '\n')

	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("каталог %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".history-*.tmp")
	if err != nil {
		return fmt.Errorf("временный файл в %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	// A conversation is the owner's text; it is not world-readable.
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("права на %s: %w", tmpName, err)
	}
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("запись %s: %w", tmpName, err)
	}
	// Sync before rename: without it the rename can land while the bytes have not,
	// and a power loss leaves an empty file where the history used to be.
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("сброс на диск %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("закрытие %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("замена %s: %w", s.path, err)
	}
	s.lastSeen = snap.Updated
	s.loaded = true
	s.sweepOrphans()
	return nil
}

// sweepOrphans removes temporary files an earlier run left behind. Every failure
// branch of Save cleans up after itself, but a process killed in the millisecond
// between CreateTemp and Rename cannot: the file stays in the sessions directory
// forever, and nothing else in the program would ever look at it.
//
// Only files older than an hour are touched, which is four orders of magnitude more
// than a live temporary file exists for — a concurrent writer's file is never at
// risk. Errors are ignored on purpose: failing to delete an orphan is housekeeping
// that did not happen, not a fact about the conversation, and turning it into an
// error would fail a turn that was written correctly.
func (s *FileStore) sweepOrphans() {
	dir := filepath.Dir(s.path)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-time.Hour)
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), ".history-") || !strings.HasSuffix(e.Name(), ".tmp") {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		os.Remove(filepath.Join(dir, e.Name()))
	}
}

// checkUnchanged refuses to write over a version this store never saw. It reads only
// the stamp, and it is not a lock: two processes writing in the same instant can
// still both pass it. What it removes is the silent case — the one where a second
// terminal quietly eats a turn and nobody finds out until the history is short.
func (s *FileStore) checkUnchanged() error {
	if !s.loaded {
		// Nothing was read, so there is nothing to contradict: a store that writes
		// without ever loading is being used as a plain sink, not as a conversation.
		return nil
	}
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		if s.lastSeen.IsZero() {
			return nil
		}
		return fmt.Errorf("история %s: %w — файл исчез", s.path, ErrChangedElsewhere)
	}
	if err != nil {
		return fmt.Errorf("история %s не читается перед записью: %w", s.path, err)
	}
	var on Snapshot
	if err := json.Unmarshal(raw, &on); err != nil {
		// Something else wrote a file we cannot parse. Overwriting it would destroy
		// whatever it is without anyone seeing it.
		return fmt.Errorf("история %s: %w — на диске лежит что-то другое", s.path, ErrChangedElsewhere)
	}
	if !on.Updated.Equal(s.lastSeen) {
		return fmt.Errorf("история %s: %w (на диске запись от %s, ожидалась от %s)",
			s.path, ErrChangedElsewhere,
			on.Updated.Local().Format("15:04:05.000"), s.lastSeen.Local().Format("15:04:05.000"))
	}
	return nil
}

// Clear forgets the conversation. A store that was never written is already clear.
//
// It resets the bookkeeping too, and that is not a detail: without it the store's own
// deliberate deletion looks to checkUnchanged like another process removing the file,
// and every write for the rest of the process is refused with a conflict that never
// happened. In the REPL that is /reset — the lesson's "conversation recreation", the
// technique the host calls very stupid and very effective — followed by an agent that
// silently stops saving until it is restarted. Found by the second review wave, which
// exists for exactly this: a fix that closes the neighbouring path instead of the real one.
func (s *FileStore) Clear() error {
	err := os.Remove(s.path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("удаление %s: %w", s.path, err)
	}
	// The file is known to be gone, which is a state this store has seen — not a
	// version someone else wrote.
	s.lastSeen = time.Time{}
	s.loaded = true
	return nil
}
