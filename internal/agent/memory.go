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
const SnapshotVersion = 1

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
	Turns    int       `json:"turns"`
	Updated  time.Time `json:"updated"`
	Messages []Message `json:"messages"`
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
}

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
	return nil
}

// Clear forgets the conversation. A store that was never written is already clear.
func (s *FileStore) Clear() error {
	err := os.Remove(s.path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("удаление %s: %w", s.path, err)
	}
	return nil
}
