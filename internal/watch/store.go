package watch

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"
)

type Store struct {
	Path        string
	LockTimeout time.Duration
	mu          sync.Mutex
}

func NewStore(path string) *Store {
	return &Store{Path: path, LockTimeout: 10 * time.Second}
}

func (s *Store) Read() (File, error) {
	var result File
	err := s.withLock(func() error {
		var err error
		result, err = s.loadUnlocked()
		return err
	})
	return result, err
}

func (s *Store) Update(change func(*File) error) error {
	return s.withLock(func() error {
		state, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		if err := change(&state); err != nil {
			return err
		}
		for i := range state.Watches {
			if len(state.Watches[i].Polls) > MaxPollsPerWatch {
				state.Watches[i].Polls = append([]Poll(nil), state.Watches[i].Polls[len(state.Watches[i].Polls)-MaxPollsPerWatch:]...)
			}
		}
		return s.saveUnlocked(state)
	})
}

func (s *Store) withLock(fn func() error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o755); err != nil {
		return err
	}
	lock, err := os.OpenFile(s.Path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	deadline := time.Now().Add(s.LockTimeout)
	for {
		err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			return err
		}
		if !time.Now().Before(deadline) {
			return errors.New("хранилище занято")
		}
		time.Sleep(10 * time.Millisecond)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	return fn()
}

func (s *Store) loadUnlocked() (File, error) {
	raw, err := os.ReadFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return EmptyFile(), nil
	}
	if err != nil {
		return File{}, s.readError(err)
	}
	var state File
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&state); err != nil {
		return File{}, s.readError(err)
	}
	if err := ensureEOF(decoder); err != nil {
		return File{}, s.readError(err)
	}
	if state.Version != Version {
		return File{}, s.readError(fmt.Errorf("неподдерживаемая версия %d", state.Version))
	}
	if state.NextID < 1 {
		state.NextID = 1
	}
	if state.Watches == nil {
		state.Watches = []Watch{}
	}
	return state, nil
}

func ensureEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("после JSON есть лишние данные")
		}
		return err
	}
	return nil
}

func (s *Store) readError(err error) error {
	return fmt.Errorf("хранилище %s не прочитано: %v; файл не изменён", s.Path, err)
}

func (s *Store) saveUnlocked(state File) error {
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	dir := filepath.Dir(s.Path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(s.Path)+".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	fail := func(cause error) error {
		_ = tmp.Close()
		return cause
	}
	if err := tmp.Chmod(0o600); err != nil {
		return fail(err)
	}
	if _, err := tmp.Write(raw); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, s.Path)
}

func (s *Store) Create(codes []string, everyMinutes int, now time.Time, poll Poll) (Watch, error) {
	var created Watch
	err := s.Update(func(state *File) error {
		active := make([]string, 0)
		for _, current := range state.Watches {
			if current.Status == "active" {
				active = append(active, current.ID)
			}
		}
		if len(active) >= MaxActiveWatches {
			return fmt.Errorf("не больше 10 активных наблюдений; активные: %s", join(active))
		}
		created = Watch{ID: "w" + strconv.Itoa(state.NextID), Codes: append([]string(nil), codes...), EveryMinutes: everyMinutes, Status: "active", CreatedAt: now.UTC().Format(time.RFC3339), Polls: []Poll{poll}}
		state.NextID++
		state.Watches = append(state.Watches, created)
		return nil
	})
	return created, err
}

func join(values []string) string {
	if len(values) == 0 {
		return "—"
	}
	result := values[0]
	for _, value := range values[1:] {
		result += ", " + value
	}
	return result
}

func (s *Store) Stop(id string, now time.Time) (Watch, error) {
	var stopped Watch
	err := s.Update(func(state *File) error {
		known := make([]string, 0, len(state.Watches))
		for i := range state.Watches {
			known = append(known, state.Watches[i].ID)
			if state.Watches[i].ID != id {
				continue
			}
			if state.Watches[i].Status == "stopped" {
				return fmt.Errorf("наблюдение %s уже остановлено", id)
			}
			state.Watches[i].Status = "stopped"
			state.Watches[i].StoppedAt = now.UTC().Format(time.RFC3339)
			stopped = state.Watches[i]
			return nil
		}
		return fmt.Errorf("наблюдение %s не найдено; известные: %s", id, join(known))
	})
	return stopped, err
}

func (s *Store) DueWatches(now time.Time) ([]Watch, error) {
	state, err := s.Read()
	if err != nil {
		return nil, err
	}
	var due []Watch
	for _, current := range state.Watches {
		if Due(current, now) {
			due = append(due, current)
		}
	}
	return due, nil
}

func (s *Store) AppendPollIfDue(id string, now time.Time, poll Poll) (bool, bool, error) {
	appended, publication := false, false
	err := s.Update(func(state *File) error {
		for i := range state.Watches {
			current := &state.Watches[i]
			if current.ID != id {
				continue
			}
			if !Due(*current, now) {
				return nil
			}
			if poll.OK {
				publication = true
				for _, previous := range current.Polls {
					if previous.OK && previous.RatesDate == poll.RatesDate {
						publication = false
						break
					}
				}
			}
			current.Polls = append(current.Polls, poll)
			appended = true
			return nil
		}
		return nil
	})
	return appended, publication, err
}
