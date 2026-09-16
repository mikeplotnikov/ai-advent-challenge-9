package agent

// Day 12: personalization on top of the memory model.
//
// The task: "создайте профиль пользователя; опишите предпочтения (стиль, формат,
// ограничения); подключите профиль к каждому запросу. Проверьте: ответы для разных
// профилей; что ассистент учитывает автоматически."
//
// A profile is NOT a memory layer. The host drew that line himself when a participant
// asked whether his personalization should move from short-term to long-term memory:
// "Нет. Память это память" (chat #2898), "А профиль это нюансы конкретного
// пользователя" (#2899). So the profile is its own entity with its own files, its own
// prompt block and its own lifetime, and day 11's long-term layer keeps only what is
// genuinely memory — decisions and knowledge. The `profile` section day 11 kept inside
// that layer moves out here; migrateLongTerm carries existing files across.
//
// Shape comes from the lesson, slide 11: STYLE (how to answer), CONSTRAINTS (what to
// respect), CONTEXT (who the user is). The blocks are separate because the measurement
// needs to ablate them one at a time — "что ассистент учитывает автоматически" is a
// question about which block did the work, not about the profile as a lump.
//
// Two host answers beyond the task text are implemented here as the smallest thing
// that honours them. Several profiles per user with a router: "его можно побить на
// файлы сделать общий профиль роутер и потом выбирать уже конкретный" (#2903) — the
// router is a deterministic substring table, never a model call. And Pipeline, one
// field with two values, for "Профиль = пайплайн из скиллов… то есть оркестрация
// скиллов" (#2913-2915): the profile may ask for a plan-then-answer pair instead of a
// single call. Whether this day requires more than that was asked in the chat (#2921)
// and had no answer by the time the day's export closed.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
)

// ProfileBlock is one of the three groups of preferences from slide 11.
type ProfileBlock string

const (
	BlockStyle       ProfileBlock = "style"
	BlockConstraints ProfileBlock = "constraints"
	BlockContext     ProfileBlock = "context"
)

// AllProfileBlocks is the injection set used when ProfileConfig.Inject is nil, and
// the order the blocks are rendered in.
var AllProfileBlocks = []ProfileBlock{BlockStyle, BlockConstraints, BlockContext}

// ProfilePipeline is how the profile wants an answer produced.
type ProfilePipeline string

const (
	// PipelineDirect is one call, the behaviour of days 6-11.
	PipelineDirect ProfilePipeline = "direct"
	// PipelinePlanAnswer is two sequential calls: a short internal plan, then the
	// answer with that plan in front of the question. The plan is never stored in
	// the conversation — it is scaffolding for one turn, not something the user said.
	PipelinePlanAnswer ProfilePipeline = "plan-answer"
)

// ProfilePipelines lists every pipeline a profile file may name.
var ProfilePipelines = []ProfilePipeline{PipelineDirect, PipelinePlanAnswer}

const (
	// ProfileVersion is the format of a profile file.
	ProfileVersion = 1
	// ProfileRouterVersion is the format of the router file.
	ProfileRouterVersion = 1
	// DefaultProfileName is the profile used when the router has nothing to say.
	DefaultProfileName = "default"
	// routerFileName is reserved: a profile may not sanitise to it.
	routerFileName = "_router"
	// maxProfileRules bounds the router the way maxMemoryEntries bounds a block.
	maxProfileRules = 32
)

var (
	ErrProfileOff       = errors.New("профиль выключен")
	ErrUnknownBlock     = errors.New("неизвестный блок профиля")
	ErrUnknownPipeline  = errors.New("неизвестный конвейер профиля")
	ErrProfileNotFound  = errors.New("такого профиля нет")
	ErrProfileKeyAbsent = errors.New("такой записи в профиле нет")
	ErrPlanFailed       = errors.New("шаг плана не удался")
)

// Block resolves a block name coming from a flag or a command.
func ParseProfileBlock(s string) (ProfileBlock, error) {
	switch b := ProfileBlock(strings.ToLower(strings.TrimSpace(s))); b {
	case BlockStyle, BlockConstraints, BlockContext:
		return b, nil
	}
	return "", fmt.Errorf("%w: %q, допустимы style, constraints, context", ErrUnknownBlock, s)
}

// ParseProfilePipeline resolves a pipeline name coming from a file or a command.
func ParseProfilePipeline(s string) (ProfilePipeline, error) {
	switch p := ProfilePipeline(strings.ToLower(strings.TrimSpace(s))); p {
	case PipelineDirect, PipelinePlanAnswer:
		return p, nil
	}
	return "", fmt.Errorf("%w: %q, допустимы direct, plan-answer", ErrUnknownPipeline, s)
}

// Profile is one configured personality of the agent for one user: how to answer,
// what to respect, and who is asking.
type Profile struct {
	Version  int             `json:"version"`
	User     string          `json:"user"`
	Name     string          `json:"name"`
	Pipeline ProfilePipeline `json:"pipeline"`
	// Stages names the day-13 StageSet this user's tasks run on. Empty means the
	// application's default. It lives here rather than in a flag because the host put
	// it here: "дальше профиль регулирует стадии и агентов" (chat #3097), and, in the
	// round videos, "мы должны ещё уметь настраивать, какие именно стадии мы хотим
	// вызывать" — the mechanism is day 13, the choice of stages is the profile.
	//
	// The field is optional, so a day-12 profile file parses unchanged.
	Stages string `json:"stages,omitempty"`
	// The three blocks reuse MemoryEntry and its limits: one line per entry, so a
	// value can never forge a neighbouring entry or a block tag.
	Style       []MemoryEntry `json:"style"`
	Constraints []MemoryEntry `json:"constraints"`
	Context     []MemoryEntry `json:"context"`
	Updated     time.Time     `json:"updated"`
}

// ProfileRule is one deterministic routing rule: if the user's message contains
// Match, case-folded, use profile Use.
type ProfileRule struct {
	Match string `json:"match"`
	Use   string `json:"use"`
}

// ProfileRouter is the "общий профиль роутер" of chat #2903. It contains no model
// call: the first matching rule wins, in file order, and Default catches the rest.
type ProfileRouter struct {
	Version int           `json:"version"`
	User    string        `json:"user"`
	Default string        `json:"default"`
	Rules   []ProfileRule `json:"rules"`
	Updated time.Time     `json:"updated"`
}

// ProfileConfig turns personalization on. It is independent of MemoryConfig on
// purpose: a user may have a profile without memory layers and layers without a
// profile. When both are set they must name the same user and directory, otherwise
// one user's files would be split across two trees.
type ProfileConfig struct {
	Dir  string
	User string
	// Name is the active profile. Empty asks the router for one.
	Name string
	// Inject selects which blocks travel in requests. Nil means all three; an empty
	// non-nil slice means the profile is stored but nothing of it is sent. Storage
	// is never affected — this is the ablation switch.
	Inject []ProfileBlock
	// Route re-resolves the profile from every incoming message through the router.
	// Off by default: with it off the system prompt is stable for the whole session,
	// which is what prefix caching wants.
	Route bool
}

// ProfileState is what an interface may show. Slices are copies.
type ProfileState struct {
	Enabled     bool
	User        string
	Name        string
	Path        string
	RouterPath  string
	Pipeline    ProfilePipeline
	Stages      string
	Inject      []ProfileBlock
	Route       bool
	Style       []MemoryEntry
	Constraints []MemoryEntry
	Context     []MemoryEntry
	Tokens      int
	Available   []string
	Rules       []ProfileRule
}

// ProfileDir is where one user's profiles live, beside their memory layers.
func ProfileDir(dir, user string) string {
	return filepath.Join(MemoryUserDir(dir, user), "profiles")
}

// profileFileName folds case. macOS and Windows resolve "Senior.json" and
// "senior.json" to one file while Linux does not, so without the fold the same profile
// name would mean one thing on the owner's laptop and another on a CI runner — and a
// name differing only in case would quietly share, or quietly corrupt, its neighbour's
// preferences. Folding makes the collision deterministic everywhere, and the ownership
// check inside the file then refuses it with a message instead of merging.
func profileFileName(name string) string {
	return strings.ToLower(sessionFileName(name))
}

func profilePath(dir, user, name string) string {
	return filepath.Join(ProfileDir(dir, user), profileFileName(name)+".json")
}

func profileRouterPath(dir, user string) string {
	return filepath.Join(ProfileDir(dir, user), routerFileName+".json")
}

// validateProfileName is validateStateName plus the one-line rule and the reserved
// router file name: two profiles that sanitise to the same file would silently share
// their preferences, and one that sanitises to _router would overwrite the router.
func validateProfileName(name string) (string, error) {
	name, err := validateStateName(name)
	if err != nil {
		return "", err
	}
	if !singleLine(name) {
		return "", fmt.Errorf("имя профиля %q: только одна строка без управляющих символов", name)
	}
	// Case-folded: on a case-insensitive filesystem "_Router" and "_router" are the
	// same file, and writing a profile over the router breaks profile selection with an
	// error that points nowhere near the cause.
	if strings.EqualFold(sessionFileName(name), routerFileName) {
		return "", fmt.Errorf("имя профиля %q зарезервировано под таблицу роутера", name)
	}
	return name, nil
}

type profileState struct {
	cfg        ProfileConfig
	inject     map[ProfileBlock]bool
	name       string
	profile    Profile
	file       layerFile
	router     ProfileRouter
	routerFile layerFile
}

func validateProfileConfig(p *ProfileConfig, m *MemoryConfig) error {
	if p == nil {
		return nil
	}
	if strings.TrimSpace(p.Dir) == "" {
		return errors.New("agent: Profile.Dir не задан")
	}
	if _, err := validateStateName(p.User); err != nil {
		return fmt.Errorf("agent: Profile.User: %w", err)
	}
	if p.Name != "" {
		if _, err := validateProfileName(p.Name); err != nil {
			return fmt.Errorf("agent: Profile.Name: %w", err)
		}
	}
	seen := map[ProfileBlock]bool{}
	for _, block := range p.Inject {
		switch block {
		case BlockStyle, BlockConstraints, BlockContext:
		default:
			return fmt.Errorf("agent: Profile.Inject: неизвестный блок %q", block)
		}
		if seen[block] {
			return fmt.Errorf("agent: Profile.Inject: блок %q указан дважды", block)
		}
		seen[block] = true
	}
	if m == nil {
		return nil
	}
	// One user, one folder. Splitting them would put the profile and the layers of
	// the same person in two trees, and /memory would describe a different user than
	// /profile does.
	if strings.TrimSpace(p.User) != strings.TrimSpace(m.User) {
		return fmt.Errorf("agent: Profile.User %q и Memory.User %q должны совпадать", p.User, m.User)
	}
	if strings.TrimSpace(p.Dir) != strings.TrimSpace(m.Dir) {
		return fmt.Errorf("agent: Profile.Dir %q и Memory.Dir %q должны совпадать", p.Dir, m.Dir)
	}
	return nil
}

func newProfileState(p ProfileConfig) *profileState {
	p.User = strings.TrimSpace(p.User)
	p.Name = strings.TrimSpace(p.Name)
	inject := map[ProfileBlock]bool{}
	blocks := p.Inject
	if blocks == nil {
		blocks = AllProfileBlocks
	}
	for _, block := range blocks {
		inject[block] = true
	}
	s := &profileState{
		cfg:        p,
		inject:     inject,
		routerFile: layerFile{path: profileRouterPath(p.Dir, p.User)},
	}
	s.setName(p.Name)
	return s
}

func (s *profileState) setName(name string) {
	s.name = name
	s.profile = Profile{}
	s.file = layerFile{}
	if name != "" {
		s.file = layerFile{path: profilePath(s.cfg.Dir, s.cfg.User, name)}
	}
}

// reload reads the router and the active profile. Like the long-term layer, both are
// re-read before every request and every write: a profile edited in another terminal
// shows up on the next turn instead of being overwritten by a stale copy.
func (s *profileState) reload() error {
	var router ProfileRouter
	found, err := s.routerFile.read(&router)
	if err != nil {
		return err
	}
	if !found {
		router = ProfileRouter{Version: ProfileRouterVersion, User: s.cfg.User, Default: DefaultProfileName}
	}
	if err := validateRouter(router, s.cfg.User); err != nil {
		return fmt.Errorf("роутер профилей %s: %w", s.routerFile.path, err)
	}
	s.router = router

	if s.name == "" {
		s.setName(router.selected(""))
	}
	var profile Profile
	found, err = s.file.read(&profile)
	if err != nil {
		return err
	}
	if !found {
		profile = Profile{Version: ProfileVersion, User: s.cfg.User, Name: s.name, Pipeline: PipelineDirect}
	}
	if err := validateProfile(profile, s.cfg.User, s.name); err != nil {
		return fmt.Errorf("профиль %s: %w", s.file.path, err)
	}
	s.profile = profile
	return nil
}

func validateProfile(p Profile, user, name string) error {
	if p.Version != ProfileVersion {
		return fmt.Errorf("версия формата %d, эта сборка понимает %d", p.Version, ProfileVersion)
	}
	if p.User != user {
		return fmt.Errorf("файл принадлежит пользователю %q, а не %q", p.User, user)
	}
	// Two profile names can sanitise to one file name; the name inside the file keeps
	// them from silently sharing preferences.
	if p.Name != name {
		// Compared through the file name, not EqualFold: Go's simple folding says
		// "İstanbul" and "istanbul" differ while ToLower maps both to istanbul.json, so
		// EqualFold would miss exactly the collision this message exists to explain.
		if profileFileName(p.Name) == profileFileName(name) {
			return fmt.Errorf("файл принадлежит профилю %q — имя %q отличается только регистром, "+
				"а файл у них один; выберите другое имя или работайте с %q", p.Name, name, p.Name)
		}
		return fmt.Errorf("файл принадлежит профилю %q, а не %q", p.Name, name)
	}
	if p.Stages != "" {
		if _, err := LookupStageSet(p.Stages); err != nil {
			return err
		}
	}
	if _, err := ParseProfilePipeline(string(p.Pipeline)); err != nil {
		return err
	}
	for _, group := range []struct {
		name    ProfileBlock
		entries []MemoryEntry
	}{{BlockStyle, p.Style}, {BlockConstraints, p.Constraints}, {BlockContext, p.Context}} {
		if err := validateEntries(group.entries); err != nil {
			return fmt.Errorf("%s: %w", group.name, err)
		}
	}
	return nil
}

func validateRouter(r ProfileRouter, user string) error {
	if r.Version != ProfileRouterVersion {
		return fmt.Errorf("версия формата %d, эта сборка понимает %d", r.Version, ProfileRouterVersion)
	}
	if r.User != user {
		return fmt.Errorf("файл принадлежит пользователю %q, а не %q", r.User, user)
	}
	if _, err := validateProfileName(r.Default); err != nil {
		return fmt.Errorf("профиль по умолчанию: %w", err)
	}
	if len(r.Rules) > maxProfileRules {
		return fmt.Errorf("правил %d, максимум %d", len(r.Rules), maxProfileRules)
	}
	for i, rule := range r.Rules {
		if strings.TrimSpace(rule.Match) == "" {
			return fmt.Errorf("правило %d: пустое условие", i+1)
		}
		if rule.Match != strings.ToLower(rule.Match) {
			return fmt.Errorf("правило %d: условие %q должно быть в нижнем регистре", i+1, rule.Match)
		}
		if _, err := validateProfileName(rule.Use); err != nil {
			return fmt.Errorf("правило %d: %w", i+1, err)
		}
	}
	return nil
}

// selected is the routing table. First match in file order wins; an input that
// matches nothing gets Default. No model is involved, so the same message always
// picks the same profile.
func (r ProfileRouter) selected(input string) string {
	folded := strings.ToLower(input)
	if folded != "" {
		for _, rule := range r.Rules {
			if strings.Contains(folded, rule.Match) {
				return rule.Use
			}
		}
	}
	if r.Default != "" {
		return r.Default
	}
	return DefaultProfileName
}

// SelectProfile reports which profile the router would choose for this message,
// without changing anything. It is what /profile route prints.
func (a *Agent) SelectProfile(input string) (string, error) {
	if a.profile == nil {
		return "", ErrProfileOff
	}
	if err := a.profile.reload(); err != nil {
		return "", err
	}
	return a.profile.router.selected(input), nil
}

// routeProfile re-resolves the active profile from the incoming message. It is the
// per-turn half of ProfileConfig.Route: the router names a profile, and the agent
// switches to it before the request is built. With Route off this never runs and the
// profile stays where /profile use put it.
func (a *Agent) routeProfile(input string) error {
	s := a.profile
	if err := s.reload(); err != nil {
		return err
	}
	chosen := s.router.selected(input)
	if chosen == s.name {
		return nil
	}
	return a.UseProfile(chosen)
}

// profileName is which profile a Reply should report. Empty means personalization is
// off, which is a different statement from "the default profile answered".
func (a *Agent) profileName() string {
	if a.profile == nil {
		return ""
	}
	return a.profile.name
}

// PlanSpend is what this conversation's planning calls have cost. Zero when no
// profile ever asked for the plan-answer pipeline.
func (a *Agent) PlanSpend() Totals {
	return a.planSpend
}

// UseProfile switches the active profile. The file is created on the first write,
// so switching to a name that has nothing saved yet is an empty profile, not an error.
func (a *Agent) UseProfile(name string) error {
	if a.profile == nil {
		return ErrProfileOff
	}
	name, err := validateProfileName(name)
	if err != nil {
		return err
	}
	s := a.profile
	previous := s.name
	previousFile := s.file
	s.setName(name)
	if err := s.reload(); err != nil {
		s.name, s.file = previous, previousFile
		if reloadErr := s.reload(); reloadErr != nil {
			return errors.Join(err, reloadErr)
		}
		return err
	}
	return nil
}

// Profiles lists the profiles this user has on disk, by file name, sorted.
func (a *Agent) Profiles() ([]string, error) {
	if a.profile == nil {
		return nil, ErrProfileOff
	}
	dir := ProfileDir(a.profile.cfg.Dir, a.profile.cfg.User)
	items, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("каталог профилей %s: %w", dir, err)
	}
	var names []string
	for _, item := range items {
		if item.IsDir() || !strings.HasSuffix(item.Name(), ".json") {
			continue
		}
		name := strings.TrimSuffix(item.Name(), ".json")
		if strings.EqualFold(name, routerFileName) {
			continue
		}
		// The name inside the file is the real one: sanitising is lossy.
		var p Profile
		f := layerFile{path: filepath.Join(dir, item.Name())}
		if found, err := f.read(&p); err != nil || !found {
			continue
		}
		names = append(names, p.Name)
	}
	sort.Strings(names)
	return names, nil
}

func (s *profileState) block(block ProfileBlock) *[]MemoryEntry {
	switch block {
	case BlockStyle:
		return &s.profile.Style
	case BlockConstraints:
		return &s.profile.Constraints
	}
	return &s.profile.Context
}

// SetPreference saves one preference into one block of the active profile. It is the
// only way anything reaches a profile file: the model never writes here, exactly as
// it never writes to the working or long-term layer.
func (a *Agent) SetPreference(block ProfileBlock, key, value string) error {
	if _, err := ParseProfileBlock(string(block)); err != nil {
		return err
	}
	key, value, err := normalizeMemoryEntry(key, value)
	if err != nil {
		return err
	}
	return a.changeProfile(func(p *Profile) error {
		entries, err := upsertEntry(*a.profile.block(block), MemoryEntry{
			Key: key, Value: value, Source: SourceCommand, Updated: time.Now(),
		})
		if err != nil {
			return err
		}
		*a.profile.block(block) = entries
		return nil
	})
}

// DropPreference deletes one preference from one block of the active profile.
func (a *Agent) DropPreference(block ProfileBlock, key string) error {
	if _, err := ParseProfileBlock(string(block)); err != nil {
		return err
	}
	key = strings.TrimSpace(key)
	return a.changeProfile(func(p *Profile) error {
		entries, err := removeEntry(*a.profile.block(block), key)
		if err != nil {
			return fmt.Errorf("%w: %q", ErrProfileKeyAbsent, key)
		}
		*a.profile.block(block) = entries
		return nil
	})
}

// SetPipeline changes how this profile wants answers produced.
// SetStageSet points this profile's tasks at a stage set. It changes nothing about
// tasks already open: a task keeps the automaton it was started on, so switching sets
// mid-task cannot renumber work that is already under way.
func (a *Agent) SetStageSet(name string) error {
	set, err := LookupStageSet(name)
	if err != nil {
		return err
	}
	return a.changeProfile(func(p *Profile) error {
		p.Stages = set.Name
		return nil
	})
}

func (a *Agent) SetPipeline(pipeline ProfilePipeline) error {
	pipeline, err := ParseProfilePipeline(string(pipeline))
	if err != nil {
		return err
	}
	return a.changeProfile(func(p *Profile) error {
		p.Pipeline = pipeline
		return nil
	})
}

func (a *Agent) changeProfile(change func(*Profile) error) error {
	if a.profile == nil {
		return ErrProfileOff
	}
	s := a.profile
	if err := s.reload(); err != nil {
		return err
	}
	before := s.profile
	if err := change(&s.profile); err != nil {
		s.profile = before
		return err
	}
	s.profile.Version = ProfileVersion
	s.profile.User = s.cfg.User
	s.profile.Name = s.name
	if s.profile.Pipeline == "" {
		s.profile.Pipeline = PipelineDirect
	}
	s.profile.Updated = time.Now()
	if err := s.file.write(s.profile); err != nil {
		s.profile = before
		return fmt.Errorf("%s: %w: %w", a.Name(), ErrNotSaved, err)
	}
	return nil
}

// SetRoute adds or replaces one routing rule. Match is folded to lower case here so
// that the file never holds a rule that can never fire.
func (a *Agent) SetRoute(match, use string) error {
	if a.profile == nil {
		return ErrProfileOff
	}
	match = strings.ToLower(strings.TrimSpace(match))
	if match == "" {
		return errors.New("условие правила пусто")
	}
	if !singleLine(match) {
		return fmt.Errorf("условие %q: только одна строка без управляющих символов", match)
	}
	use, err := validateProfileName(use)
	if err != nil {
		return err
	}
	return a.changeRouter(func(r *ProfileRouter) error {
		for i := range r.Rules {
			if r.Rules[i].Match == match {
				r.Rules[i].Use = use
				return nil
			}
		}
		if len(r.Rules) >= maxProfileRules {
			return fmt.Errorf("в роутере уже %d правил — это максимум", maxProfileRules)
		}
		r.Rules = append(r.Rules, ProfileRule{Match: match, Use: use})
		return nil
	})
}

// SetDefaultProfile is which profile the router falls back to.
func (a *Agent) SetDefaultProfile(name string) error {
	name, err := validateProfileName(name)
	if err != nil {
		return err
	}
	return a.changeRouter(func(r *ProfileRouter) error {
		r.Default = name
		return nil
	})
}

func (a *Agent) changeRouter(change func(*ProfileRouter) error) error {
	if a.profile == nil {
		return ErrProfileOff
	}
	s := a.profile
	if err := s.reload(); err != nil {
		return err
	}
	before := s.router
	if err := change(&s.router); err != nil {
		s.router = before
		return err
	}
	s.router.Version = ProfileRouterVersion
	s.router.User = s.cfg.User
	if s.router.Default == "" {
		s.router.Default = DefaultProfileName
	}
	s.router.Updated = time.Now()
	if err := s.routerFile.write(s.router); err != nil {
		s.router = before
		return fmt.Errorf("%s: %w: %w", a.Name(), ErrNotSaved, err)
	}
	return nil
}

// ProfileQuestion is one step of the initialization interview.
type ProfileQuestion struct {
	Block ProfileBlock `json:"block"`
	Key   string       `json:"key"`
	Ask   string       `json:"ask"`
	Hint  string       `json:"hint"`
}

// ProfileInterview is the fixed list of questions /profile init walks through. The
// lesson suggests an interview at initialization — "если вам пишет новый ID… проведи
// с ним базовое интервью" (slide 11) — and this one costs no model call: the questions
// are constants and the answers go straight into the blocks. A deterministic interview
// is also the only kind a test can replay.
func ProfileInterview() []ProfileQuestion {
	return []ProfileQuestion{
		{BlockContext, "role", "Кто вы и в какой роли задаёте вопросы?", "например: senior Android-разработчик"},
		{BlockContext, "project", "Над чем работаете?", "например: мобильный банк, команда 4 человека"},
		{BlockStyle, "language", "На каком языке отвечать?", "например: русский"},
		{BlockStyle, "detail", "Насколько подробно отвечать?", "например: коротко, без вводных абзацев"},
		{BlockStyle, "code", "Нужны ли примеры кода?", "например: да, минимальные, без псевдокода"},
		{BlockConstraints, "stack", "Какого стека держаться?", "например: только Kotlin и стандартная библиотека"},
		{BlockConstraints, "avoid", "Чего избегать?", "например: не предлагать смену стека"},
	}
}

// InitProfile writes the answers of one interview into the active profile. Empty
// answers are skipped: a question the user did not answer must not become a
// preference that says nothing.
func (a *Agent) InitProfile(answers map[string]string) (int, error) {
	if a.profile == nil {
		return 0, ErrProfileOff
	}
	written := 0
	for _, q := range ProfileInterview() {
		value := strings.TrimSpace(answers[q.Key])
		if value == "" {
			continue
		}
		if err := a.SetPreference(q.Block, q.Key, value); err != nil {
			return written, fmt.Errorf("%s: %w", q.Key, err)
		}
		written++
	}
	return written, nil
}

// ProfileExists reports whether this user already has the named profile on disk. The
// interface uses it to decide whether a new user should be interviewed.
func (a *Agent) ProfileExists() (bool, error) {
	if a.profile == nil {
		return false, ErrProfileOff
	}
	return a.profile.file.exists()
}

// ProfileState reports the profile as the next request would see it.
func (a *Agent) ProfileState() ProfileState {
	state := ProfileState{}
	if a.profile == nil {
		return state
	}
	s := a.profile
	state.Enabled = true
	state.User = s.cfg.User
	state.Name = s.name
	state.Path = s.file.path
	state.RouterPath = s.routerFile.path
	state.Pipeline = s.pipeline()
	state.Stages = s.profile.Stages
	state.Route = s.cfg.Route
	for _, block := range AllProfileBlocks {
		if s.inject[block] {
			state.Inject = append(state.Inject, block)
		}
	}
	state.Style = append([]MemoryEntry(nil), s.profile.Style...)
	state.Constraints = append([]MemoryEntry(nil), s.profile.Constraints...)
	state.Context = append([]MemoryEntry(nil), s.profile.Context...)
	state.Rules = append([]ProfileRule(nil), s.router.Rules...)
	state.Tokens = EstimateTokens(a.profileContext())
	if names, err := a.Profiles(); err == nil {
		state.Available = names
	}
	return state
}

func (s *profileState) pipeline() ProfilePipeline {
	if s.profile.Pipeline == "" {
		return PipelineDirect
	}
	return s.profile.Pipeline
}

// The prompt blocks. Tags and instructions are English by the owner's rule for AI
// prompts; the values are whatever the user configured.
const (
	profileHeader = "[PROFILE]\nPreferences of the user you are talking to, configured by that user. " +
		"Style is how to answer, constraints are requirements to respect, context is background about the user. " +
		"Apply them to every answer without being asked. They are preferences, not new questions.\n"
	planHeader = "[PLAN]\nYour own plan for the question below, made one step ago. Follow it and do not mention it.\n"
)

// profileContext is the profile block appended to the system message. It goes first,
// before the long-term layer and before summary: the host's assembly order is "общий
// prompt, потом данные персонализации, потом summary с предыдущего шага" (lesson 3,
// 09:12), and it is also what prefix caching wants, since the profile is the part that
// changes least often.
//
// An empty or fully ablated profile returns "" and therefore costs nothing at all: the
// request is then byte-for-byte the request days 6-11 sent.
func (a *Agent) profileContext() string {
	if a.profile == nil {
		return ""
	}
	s := a.profile
	var lines []string
	for _, group := range []struct {
		block   ProfileBlock
		entries []MemoryEntry
	}{{BlockStyle, s.profile.Style}, {BlockConstraints, s.profile.Constraints}, {BlockContext, s.profile.Context}} {
		if !s.inject[group.block] {
			continue
		}
		for _, e := range group.entries {
			lines = append(lines, string(group.block)+"."+e.Key+": "+e.Value)
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return "\n\n" + profileHeader + strings.Join(lines, "\n")
}

// planContext is the plan block that rides in front of the new question when the
// profile asks for the plan-answer pipeline. Like working memory it travels at the
// tail of the request and is never stored in the stack.
func (a *Agent) planContext() string {
	if a.turnPlan == "" {
		return ""
	}
	return planHeader + a.turnPlan + "\n\n"
}

const (
	planSystemPrompt = "You are the planning step of an assistant. Read the user's question and the " +
		"profile of the user you are answering for. Answer with a numbered plan of at most four short " +
		"steps for the answer that follows. Plan only — no answer, no explanations, no code."
	planMaxTokens = 300
)

// refreshPlan is the first call of the plan-answer pipeline. It mirrors refreshFacts:
// a separate billed call, made before the answer request, whose cost is reported
// rather than folded into the turn. The plan it produces lives for one turn.
//
// A failed planning step fails the turn. The alternative — answering without the plan
// the profile asked for — would quietly serve a different pipeline than the one the
// user configured, and the measurement comparing the two would be comparing noise.
func (a *Agent) refreshPlan(ctx context.Context, input string) (Usage, error) {
	a.turnPlan = ""
	if a.profile == nil || a.profile.pipeline() != PipelinePlanAnswer {
		return Usage{}, nil
	}
	zero := 0.0
	system := planSystemPrompt + a.profileContext()
	answer, callErr := a.client.AskWith(ctx, []llm.Message{
		{Role: "system", Content: system},
		{Role: "user", Content: input},
	}, llm.Options{
		MaxTokens:   planMaxTokens,
		Temperature: &zero,
		Thinking:    "disabled",
	})
	usage := a.usage(answer)
	if callErr != nil {
		a.recordPlan(usage, true)
		return usage, fmt.Errorf("%w: вызов модели: %v", ErrPlanFailed, callErr)
	}
	plan := strings.TrimSpace(answer.Content)
	if plan == "" {
		a.recordPlan(usage, true)
		return usage, fmt.Errorf("%w: пустой план", ErrPlanFailed)
	}
	if !singleBlock(plan) {
		a.recordPlan(usage, true)
		return usage, fmt.Errorf("%w: план содержит запрещённые символы", ErrPlanFailed)
	}
	a.recordPlan(usage, false)
	a.turnPlan = plan
	return usage, nil
}

// singleBlock is the one-line rule of entry values, relaxed to allow ordinary line
// breaks: a plan is a list. What it still rejects is U+2028/U+2029 and control
// characters other than \n and \t, so a plan cannot forge the [USER_MESSAGE] tag by
// smuggling an exotic separator past the renderer.
func singleBlock(s string) bool {
	for _, r := range s {
		if r == '\n' || r == '\t' {
			continue
		}
		if !singleLine(string(r)) {
			return false
		}
	}
	return !strings.Contains(s, userMessageTag)
}

func (a *Agent) recordPlan(u Usage, failed bool) {
	a.record(u, failed)
	addUsage(&a.planSpend, u, failed)
}

// migrateLongTerm moves day 11's `profile` section out of the long-term layer and
// into the day-12 profile, then rewrites the layer in the new format. It runs once,
// at construction, before anything reads either file.
//
// The order matters for a crash in between: the profile is written first, so a second
// run finds the entries already there and skips them rather than losing them. Keys the
// target profile already has are never overwritten — a preference the user set today
// outranks one migrated from yesterday's layer.
func migrateLongTerm(dir, user string) error {
	file := layerFile{path: longTermPath(dir, user)}
	var legacy longTermV1
	found, err := file.read(&legacy)
	if err != nil || !found {
		// Not readable as v1 means it is already v2 (or broken, which the ordinary
		// read will report with its own message). Nothing to migrate.
		return nil
	}
	if legacy.Version != 1 {
		return nil
	}
	if len(legacy.Profile) > 0 {
		target := layerFile{path: profilePath(dir, user, DefaultProfileName)}
		var p Profile
		if _, err := target.read(&p); err != nil {
			return err
		}
		if p.Version == 0 {
			p = Profile{Version: ProfileVersion, User: user, Name: DefaultProfileName, Pipeline: PipelineDirect}
		}
		have := map[string]bool{}
		for _, e := range p.Style {
			have[e.Key] = true
		}
		for _, e := range legacy.Profile {
			if have[e.Key] {
				continue
			}
			moved, err := upsertEntry(p.Style, e)
			if err != nil {
				return fmt.Errorf("перенос профиля из %s: %w", file.path, err)
			}
			p.Style = moved
		}
		p.Updated = time.Now()
		if err := target.write(p); err != nil {
			return fmt.Errorf("перенос профиля в %s: %w", target.path, err)
		}
	}
	upgraded := LongTermMemory{
		Version:   LongTermVersion,
		User:      legacy.User,
		Decisions: legacy.Decisions,
		Knowledge: legacy.Knowledge,
		Updated:   time.Now(),
	}
	if err := file.write(upgraded); err != nil {
		return fmt.Errorf("обновление формата %s: %w", file.path, err)
	}
	return nil
}

// memoryLocation is the user's folder as either config names it. Both may name it;
// New has already refused a config where the two disagree.
func memoryLocation(cfg Config) (dir, user string, ok bool) {
	switch {
	case cfg.Memory != nil:
		return cfg.Memory.Dir, cfg.Memory.User, true
	case cfg.Profile != nil:
		return cfg.Profile.Dir, cfg.Profile.User, true
	}
	return "", "", false
}

// longTermV1 is the day-11 long-term file: the one that still kept the profile inside
// the memory layer. It exists only so the migration can read it.
type longTermV1 struct {
	Version   int           `json:"version"`
	User      string        `json:"user"`
	Profile   []MemoryEntry `json:"profile"`
	Decisions []MemoryEntry `json:"decisions"`
	Knowledge []MemoryEntry `json:"knowledge"`
	Updated   time.Time     `json:"updated"`
}
