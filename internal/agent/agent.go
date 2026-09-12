// Package agent is the day-6 deliverable: the agent as a thing of its own, not a
// function that happens to call an API.
//
// The task says only "агент должен быть отдельной сущностью, а не просто один вызов
// API; логика запроса и ответа должна быть инкапсулирована в агенте". The host filled
// in what that means in the challenge chat on 2026-09-07:
//
//   - the agent takes on assembling the message stack itself and forwarding it to the
//     LLM (msg 2127, answering whether a class holding the system prompt, temperature
//     and max_tokens, returning only the result, is the right shape: "Да все так,
//     также она должна сама на себя брать складывание стека сообщений и пересылку ллмке");
//   - the agent is a box with an input policy, an output policy and its own judge if
//     it needs one, and may be an isolated module rather than a REST service
//     (msg 2144, endorsed with "Да все так" in 2150);
//   - everything week 1 played with belongs in the agent's config, token accounting
//     included (msg 2152: "Я бы туда сразу добавил подсчет токенов и вообще все что
//     мы делали в прошлой неделе в виде конфига агента").
//
// So the package boundary is the point of the exercise: callers hand this package a
// string and get a Reply. They never see llm.Message, llm.Options or an HTTP status,
// and day-06's CLI does not import internal/llm at all — enforced by a test, because
// otherwise "encapsulated" is a word in a report rather than a property of the code.
package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
)

// Config is the agent's whole configuration: who it is, which model it speaks to,
// and every generation control week 1 measured. A zero value is usable — it means
// "no system prompt, provider defaults everywhere", which is what days 1-2 sent.
type Config struct {
	// Name is what the interface calls this agent. Cosmetic; it never reaches the API.
	Name string
	// SystemPrompt is the role and instructions. The lesson's CONTEXT SPECIFIC slide
	// puts "roles and instructions" alongside top_p/top_k as the way to narrow the
	// scope the model answers from.
	SystemPrompt string
	// Model is the name the answer is priced under when the API does not echo one
	// back. The agent does not choose the model: which endpoint and which model to
	// talk to is settled when the application builds the transport, and this field
	// must name that same model. Day 5 is the reason it is here at all — pricing
	// depends on the model and on the hour of the call.
	Model string

	// The week-1 controls, in the agent's config because the host said to put them
	// there. Each is sent only when set, so a zero Config sends none of them.
	Temperature     *float64
	MaxTokens       int
	Stop            []string
	ResponseFormat  string
	Thinking        string
	ReasoningEffort string

	// MaxTurns caps how many past exchanges travel in the stack. 0 means "all of
	// them" — which is the honest day-6 default: this is where sliding window
	// would go, and it is not day 6's job to pretend it is already there.
	MaxTurns int

	// KeepLastMessages is day 9's context compression policy. When it is positive,
	// the agent keeps exactly this many newest messages verbatim and replaces every
	// older complete exchange with one separately stored summary. It must be even:
	// a conversation is made of user/assistant pairs, and retaining a lone question
	// would tell the model that an earlier assistant ignored it.
	//
	// 0 keeps the day-8 behaviour unchanged. It is deliberately incompatible with
	// MaxTurns and OverflowTrim: compression preserves old context semantically,
	// while those policies delete it.
	KeepLastMessages int

	// ContextStrategy is day 10's explicit alternative to summary. The three
	// strategies are mutually exclusive and keep their own persisted state.
	ContextStrategy ContextStrategy
	// WindowMessages is the raw-message limit used by sliding and facts. It counts
	// individual user/assistant messages, not exchanges, and must therefore be even.
	WindowMessages int

	// MaxContextTokens is day 8: the agent's own ceiling on what one request may
	// weigh, in tokens, measured by the local estimator before the call. 0 means no
	// ceiling of ours — the provider's window is then the only limit, and the
	// provider is the one who says no.
	//
	// It is deliberately not preset to the model's window. That window is a fact
	// about the provider (1M for both v4 models, per its pricing page), while this
	// is a policy of ours, and conflating the two would hide which of them refused.
	MaxContextTokens int
	// OnOverflow is what to do when the estimate exceeds MaxContextTokens: refuse,
	// trim the oldest exchanges, or send anyway with a warning. Empty means refuse.
	// It has no meaning without a ceiling, and New rejects it without one rather
	// than letting a configured policy sit there doing nothing.
	OnOverflow OverflowPolicy

	// Store is day 7: where the conversation lives between runs. Nil means the
	// agent forgets everything when the process ends, which is exactly what day 6
	// did. Set it and the agent loads its history when it is built and writes it
	// after every completed turn — "продолжайте диалог так, как будто агент не
	// выключался" is then a property of the agent, not of the interface around it.
	Store Store

	// Validate is the output policy: it inspects the model's text and returns an
	// error when the answer is unusable. Nil means "any non-empty answer is fine".
	// Day 2 learned that response_format guarantees valid JSON and not your schema;
	// this is the hook where that check lives.
	Validate func(content string) error
}

// Reply is what the agent hands back: the answer plus everything needed to say what
// it cost. Nothing here leaks the transport — no status codes, no raw JSON.
type Reply struct {
	Text     string
	Model    string
	Turn     int
	Elapsed  time.Duration
	Usage    Usage
	Reasoned bool

	// Estimated is what the agent thought this request would weigh before sending
	// it. Kept next to Usage on purpose: the pair is the measurement of the local
	// counter, and every turn produces one for free.
	Estimated Estimate
	// Dropped is how many oldest exchanges the ceiling policy removed from the
	// conversation to make this request fit. Non-zero means the agent forgot
	// something in order to answer, which is a fact the interface must not hide.
	Dropped int
	// Warning is the ceiling policy speaking: history was trimmed, or an over-sized
	// request was sent anyway. Empty on an ordinary turn.
	Warning string
	// Truncated reports that the model stopped because it ran into the generation
	// cap rather than because it had finished — finish_reason "length". The answer
	// is then a fragment, and day 2's lesson applies: what a truncated JSON object
	// breaks is the parser, not the API call.
	Truncated bool
	// FactUsage is the separate provider call which updated sticky facts before the
	// answer. It is also included in the conversation Totals, never hidden as free.
	FactUsage     Usage
	FactAttempted bool
}

// Usage is the agent's own token accounting, cache split included: the difference
// between a repeated prompt costing $0.007 and $0.22 per 1M is exactly this split,
// and days 1-4 never read it.
type Usage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	CachedTokens     int
	MissedTokens     int
	// ReasoningTokens are billed as output while never appearing in the answer. A
	// report that leaves them out understates what a reasoning model costs.
	ReasoningTokens int
	// Cost is in dollars, priced at the moment of the call: the provider's rates
	// double during peak hours, so a price computed later is a different number.
	Cost float64
	// Priced is false when the model is not in the price table — then Cost is 0
	// because it is unknown, not because the call was free.
	Priced bool
}

// ErrEmptyAnswer is returned when the model replies with nothing. Day 5 established
// that an empty answer is a failed call on both sides and is billed anyway, so the
// agent refuses to pass it off as a result.
var ErrEmptyAnswer = errors.New("модель вернула пустой ответ")

// ErrEmptyInput is returned when the caller asks nothing.
var ErrEmptyInput = errors.New("пустой запрос")

// Caller is the transport the agent talks through. It exists so the agent can be
// tested without a network and without a key: the tests substitute a fake, and the
// production wiring passes an *llm.Client.
type Caller interface {
	AskWith(ctx context.Context, messages []llm.Message, opts llm.Options) (llm.Answer, error)
}

// Agent holds the config and the conversation stack. It is not safe for concurrent
// use: the stack is mutable state, and one agent is one conversation.
type Agent struct {
	cfg    Config
	client Caller
	// stack is the conversation without the system prompt, which is prepended at
	// send time. Keeping it out means changing the role mid-conversation cannot
	// leave a stale system message buried in the history.
	stack []llm.Message
	// summary is the compressed part of the conversation. It is never appended to
	// stack or saved as a normal turn: a later request puts it into its own marked
	// context section, so the raw recent tail remains inspectable and exact.
	summary            string
	compressedMessages int
	summarySpend       Totals
	facts              map[string]string
	factSpend          Totals
	activeBranch       string
	inactiveBranches   map[string][]llm.Message
	checkpoints        map[string][]llm.Message
	turns              int
	// restored is what Load found when this agent was built, kept so the interface
	// can say "загружено N ходов" without asking the store a second time.
	restored Restored
	// totals is what this conversation has been billed, across restarts. It is the
	// agent's, not the interface's: a total kept by whoever happens to be printing
	// it would reset every time the process does, and day 7 made restarts routine.
	totals Totals
}

// Restored describes the conversation the agent woke up with. Warnings are the
// discrepancies that do not justify refusing to continue but do change what the
// history means — a system prompt or a model that is not the one the history was
// recorded under.
type Restored struct {
	Turns    int
	Messages int
	Updated  time.Time
	Warnings []string
}

// New builds an agent over a transport the caller supplies. This is the injection
// point — tests pass a fake, and an application that wants to own the client passes
// its own. An interface that has no business knowing what a client is asks FromEnv
// instead; see wiring.go.
func New(client Caller, cfg Config) (*Agent, error) {
	if client == nil {
		return nil, errors.New("agent: клиент не задан")
	}
	if cfg.MaxTurns < 0 {
		return nil, fmt.Errorf("agent: MaxTurns = %d, отрицательным быть не может", cfg.MaxTurns)
	}
	if cfg.KeepLastMessages < 0 {
		return nil, fmt.Errorf("agent: KeepLastMessages = %d, отрицательным быть не может", cfg.KeepLastMessages)
	}
	if cfg.KeepLastMessages > 0 && (cfg.KeepLastMessages < 2 || cfg.KeepLastMessages%2 != 0) {
		return nil, fmt.Errorf("agent: KeepLastMessages = %d, нужны минимум два и только чётное число сообщений", cfg.KeepLastMessages)
	}
	if cfg.KeepLastMessages > 0 && cfg.MaxTurns > 0 {
		return nil, errors.New("agent: KeepLastMessages и MaxTurns нельзя включать вместе: сжатие сохраняет прошлое, а окно его удаляет")
	}
	if err := validateContextStrategy(cfg); err != nil {
		return nil, err
	}
	if cfg.MaxContextTokens < 0 {
		return nil, fmt.Errorf("agent: MaxContextTokens = %d, отрицательным быть не может", cfg.MaxContextTokens)
	}
	switch cfg.OnOverflow {
	case "", OverflowRefuse, OverflowTrim, OverflowWarn:
	default:
		return nil, fmt.Errorf("agent: OnOverflow = %q, допустимы %q, %q и %q",
			cfg.OnOverflow, OverflowRefuse, OverflowTrim, OverflowWarn)
	}
	if cfg.KeepLastMessages > 0 && cfg.OnOverflow == OverflowTrim {
		return nil, errors.New("agent: KeepLastMessages и OnOverflow=trim нельзя включать вместе: сжатие сохраняет прошлое, а trim его удаляет")
	}
	if cfg.OnOverflow != "" && cfg.MaxContextTokens == 0 {
		return nil, fmt.Errorf("agent: OnOverflow = %q без MaxContextTokens — политика без потолка никогда не сработает",
			cfg.OnOverflow)
	}
	a := &Agent{cfg: cfg, client: client}
	a.resetStrategyState()
	if err := a.restore(); err != nil {
		return nil, err
	}
	return a, nil
}

// restore loads the stored conversation into the agent. A store that has nothing
// saved is the ordinary first run. A store that cannot be read is a hard failure:
// the alternative — starting empty — would silently discard a conversation that is
// still on disk, and would do it exactly when someone restarts the agent expecting
// it to remember.
func (a *Agent) restore() error {
	if a.cfg.Store == nil {
		return nil
	}
	snap, err := a.cfg.Store.Load()
	if err != nil {
		return fmt.Errorf("%s: %w", a.Name(), err)
	}
	if len(snap.Messages) == 0 && snap.Turns == 0 && snap.Strategy == "" {
		return nil
	}
	if err := a.restoreStrategy(snap); err != nil {
		return fmt.Errorf("%s: %w", a.Name(), err)
	}
	// A compressed snapshot has intentionally discarded its old verbatim exchanges.
	// Turning compression off later cannot turn that summary back into the full
	// dialogue, so continuing would make -keep-last=0 lie about what it sends.
	if a.cfg.KeepLastMessages == 0 && snap.CompressedMessages > 0 {
		return fmt.Errorf("%s: беседа уже сжата; -keep-last=0 не может восстановить удалённую дословную историю. Используй новую беседу для контроля без сжатия", a.Name())
	}

	a.stack = make([]llm.Message, 0, len(snap.Messages))
	for _, m := range snap.Messages {
		a.stack = append(a.stack, llm.Message{Role: m.Role, Content: m.Content})
	}
	a.turns = snap.Turns
	a.totals = snap.Spend
	a.summary = snap.Summary
	a.compressedMessages = snap.CompressedMessages
	a.summarySpend = snap.SummarySpend
	// The window applies to loaded history too. Otherwise -max-turns would cap what
	// this run adds while quietly sending an unbounded history from the file.
	a.trim()

	a.restored = Restored{
		Turns:    a.turns,
		Messages: len(a.stack),
		Updated:  snap.Updated,
	}
	if snap.System != a.cfg.SystemPrompt {
		a.restored.Warnings = append(a.restored.Warnings,
			"системный промпт изменился с прошлого запуска — история записана под другой ролью")
	}
	if snap.Model != "" && a.cfg.Model != "" && snap.Model != a.cfg.Model {
		a.restored.Warnings = append(a.restored.Warnings,
			fmt.Sprintf("история записана на модели %s, сейчас %s", snap.Model, a.cfg.Model))
	}
	return nil
}

// Restored is what the agent found in its store when it was built.
func (a *Agent) Restored() Restored { return a.restored }

// Remembers reports whether this agent keeps its conversation between runs. An
// interface uses it to say so out loud instead of leaving it to be inferred.
func (a *Agent) Remembers() bool { return a.cfg.Store != nil }

// Name is what to call this agent in an interface.
func (a *Agent) Name() string {
	if a.cfg.Name == "" {
		return "агент"
	}
	return a.cfg.Name
}

// Turns is how many completed exchanges the agent is carrying.
func (a *Agent) Turns() int { return a.turns }

// Reset clears the conversation. This is "conversation recreation" from the lesson —
// the lever the host calls "очень тупая и очень эффективная техника" and recommends
// pulling whenever the next task is genuinely a new one.
//
// It clears the store as well: a conversation the agent has been told to forget must
// not come back on the next start, which is the whole difference day 7 introduces.
func (a *Agent) Reset() error {
	a.stack = nil
	a.summary = ""
	a.compressedMessages = 0
	a.summarySpend = Totals{}
	a.resetStrategyState()
	a.turns = 0
	a.restored = Restored{}
	// The spend goes with the conversation it belongs to. Keeping it would report
	// what a previous conversation cost as the cost of this one.
	a.totals = Totals{}
	if a.cfg.Store == nil {
		return nil
	}
	if err := a.cfg.Store.Clear(); err != nil {
		return fmt.Errorf("%s: %w", a.Name(), err)
	}
	return nil
}

// Ask runs one exchange: input policy, the context ceiling, assemble the stack, call
// the model, output policy, and only then commit the turn to the conversation.
func (a *Agent) Ask(ctx context.Context, input string) (Reply, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return Reply{}, ErrEmptyInput
	}

	factUsage, factErr := a.refreshFacts(ctx, input)
	factAttempted := a.cfg.ContextStrategy == ContextFacts
	if factErr != nil {
		base := Reply{FactUsage: factUsage, FactAttempted: factAttempted}
		return a.persistFailedReply(base, factErr, nil)
	}

	// A version-1/2 session can wake up with more raw messages than today's policy
	// permits. Compress before assembling this request, not only after its answer:
	// otherwise the very first post-upgrade request would still send the full legacy
	// history. A failed attempt leaves the raw stack untouched and is reported after
	// the usable answer has been persisted.
	preCompressionAttempt := a.cfg.KeepLastMessages > 0 && len(a.stack) > a.cfg.KeepLastMessages
	compressionErr := a.compress(ctx)

	// Day 8: the ceiling is checked before the call, because after the call the
	// money is already spent. A refusal here costs nothing at all.
	send, dropped, warning, est, err := a.fit(input)
	if err != nil {
		base := Reply{Estimated: est, FactUsage: factUsage, FactAttempted: factAttempted}
		// A preflight refusal still happens after an attempted compression when a
		// legacy session was too long. Persist that attempt (including any billed
		// failed summary call) before returning. Facts mode deliberately updates its
		// memory after every user message, so that separate call can also have been
		// billed before the answer request is refused; expose and persist it.
		if preCompressionAttempt || a.cfg.ContextStrategy == ContextFacts {
			if persistErr := a.persist(); persistErr != nil {
				return base, errors.Join(err, persistErr)
			}
		}
		if compressionErr != nil {
			return base, errors.Join(err, compressionErr)
		}
		return base, err
	}

	messages := a.messagesWith(send, input)

	started := time.Now()
	answer, err := a.client.AskWith(ctx, messages, a.options())
	elapsed := time.Since(started)
	usage := a.usage(answer)
	truncated := answer.FinishReason == finishLength
	base := Reply{
		Model:     answer.Model,
		Elapsed:   elapsed,
		Usage:     usage,
		Estimated: est,
		Warning:   warning,
		Truncated: truncated,
		FactUsage: factUsage, FactAttempted: factAttempted,
	}
	if err != nil {
		// A failed call can still have been billed — a request the provider refused,
		// an answer that never arrived. Whatever it reported is recorded rather than
		// dropped: a conversation's total that omits its failures under-reports the
		// spend exactly where the day is about spend.
		a.record(usage, true)
		return a.persistFailedReply(base, fmt.Errorf("%s: вызов модели не удался: %w", a.Name(), err), compressionErr)
	}

	text := strings.TrimSpace(answer.Content)
	if text == "" {
		// The call was still billed, so the caller is told what it cost even though
		// the exchange failed.
		a.record(usage, true)
		return a.persistFailedReply(base, ErrEmptyAnswer, compressionErr)
	}
	if a.cfg.Validate != nil {
		if err := a.cfg.Validate(text); err != nil {
			a.record(usage, true)
			return a.persistFailedReply(base, fmt.Errorf("%s: ответ не прошёл проверку: %w", a.Name(), err), compressionErr)
		}
	}
	a.record(usage, false)

	// The trim decided by the ceiling takes effect only now, together with the turn
	// that needed it: history dropped for a call that then failed would be history
	// lost for nothing.
	a.stack = send

	// The turn joins the conversation only once it is known to be usable: a rejected
	// answer must not poison the next request's context.
	a.stack = append(a.stack,
		llm.Message{Role: "user", Content: input},
		llm.Message{Role: "assistant", Content: text},
	)
	a.turns++
	a.trim()

	reply := base
	reply.Text = text
	reply.Turn = a.turns
	reply.Dropped = dropped
	reply.Reasoned = answer.Reasoned()

	// Compression runs only after a complete, usable exchange has joined the
	// conversation. If its own model call fails, the raw messages remain intact and
	// are persisted below; losing them before a summary exists would turn an
	// optimisation into silent data loss.
	if compressionErr == nil {
		compressionErr = a.compress(ctx)
	}

	// Persisting is part of the turn, not an afterthought at exit: a process killed
	// between two questions must lose nothing. The reply is returned in full even
	// when the write fails — the call happened and was billed — but the failure
	// travels with it, because an agent that has stopped saving looks exactly like
	// one that is saving right up until it is restarted.
	if err := a.persist(); err != nil {
		return reply, err
	}
	if compressionErr != nil {
		return reply, compressionErr
	}
	return reply, nil
}

// persistFailedReply keeps every already-billed state change across a restart. A
// preflight compression can successfully replace old raw context before the answer
// request; if that request then fails, skipping Save would repeat the summary call
// and make the stored cost ledger less than the provider has charged.
func (a *Agent) persistFailedReply(reply Reply, cause, compressionErr error) (Reply, error) {
	if err := a.persist(); err != nil {
		cause = errors.Join(cause, err)
	}
	if compressionErr != nil {
		cause = errors.Join(cause, compressionErr)
	}
	return reply, cause
}

// finishLength is the provider's word for "stopped because it ran out of room",
// as opposed to "stopped because it was done". It stays inside this package: the
// interface is told Truncated, not a transport's vocabulary.
const finishLength = "length"

// persist writes the conversation as it now stands.
func (a *Agent) persist() error {
	if a.cfg.Store == nil {
		return nil
	}
	snap := Snapshot{
		Version:            SnapshotVersion,
		Agent:              a.Name(),
		Model:              a.cfg.Model,
		System:             a.cfg.SystemPrompt,
		Turns:              a.turns,
		Updated:            time.Now(),
		Spend:              a.totals,
		Summary:            a.summary,
		CompressedMessages: a.compressedMessages,
		SummarySpend:       a.summarySpend,
		Strategy:           a.cfg.ContextStrategy,
		WindowMessages:     a.cfg.WindowMessages,
		Facts:              cloneFacts(a.facts),
		FactSpend:          a.factSpend,
		ActiveBranch:       a.activeBranch,
		Branches:           storedMessages(a.inactiveBranches),
		Checkpoints:        storedMessages(a.checkpoints),
		Messages:           make([]Message, 0, len(a.stack)),
	}
	for _, m := range a.stack {
		snap.Messages = append(snap.Messages, Message{Role: m.Role, Content: m.Content})
	}
	if err := a.cfg.Store.Save(snap); err != nil {
		return fmt.Errorf("%s: %w: %w", a.Name(), ErrNotSaved, err)
	}
	return nil
}

// messagesFor builds the stack that goes over the wire: system prompt, the kept
// history, then the new input. Returned fresh each time so a caller holding an
// earlier slice cannot observe it change underneath.
func (a *Agent) messagesFor(input string) []llm.Message {
	return a.messagesWith(a.stack, input)
}

// messagesWith is messagesFor over a stack the caller chose — the ceiling policy
// sends a trimmed one without the agent having committed to the trim yet.
func (a *Agent) messagesWith(stack []llm.Message, input string) []llm.Message {
	out := make([]llm.Message, 0, len(stack)+2)
	if system := a.contextSystemPrompt(); system != "" {
		out = append(out, llm.Message{Role: "system", Content: system})
	}
	out = append(out, stack...)
	return append(out, llm.Message{Role: "user", Content: input})
}

// trim drops the oldest exchanges once the conversation is longer than MaxTurns.
// Whole exchanges, never a lone user message: a user turn without its answer would
// read to the model as a question it ignored.
func (a *Agent) trim() {
	keep := a.cfg.MaxTurns * 2
	if a.cfg.ContextStrategy == ContextSliding || a.cfg.ContextStrategy == ContextFacts {
		keep = a.cfg.WindowMessages
	}
	if keep <= 0 {
		return
	}
	if len(a.stack) > keep {
		a.stack = append([]llm.Message(nil), a.stack[len(a.stack)-keep:]...)
	}
}

func (a *Agent) options() llm.Options {
	return llm.Options{
		MaxTokens:       a.cfg.MaxTokens,
		Stop:            a.cfg.Stop,
		ResponseFormat:  a.cfg.ResponseFormat,
		Temperature:     a.cfg.Temperature,
		Thinking:        a.cfg.Thinking,
		ReasoningEffort: a.cfg.ReasoningEffort,
	}
}

func (a *Agent) usage(answer llm.Answer) Usage {
	u := Usage{
		PromptTokens:     answer.Usage.PromptTokens,
		CompletionTokens: answer.Usage.CompletionTokens,
		TotalTokens:      answer.Usage.TotalTokens,
		CachedTokens:     answer.Usage.PromptCacheHitTokens,
		MissedTokens:     answer.Usage.PromptCacheMissTokens,
		ReasoningTokens:  answer.Usage.CompletionDetails.ReasoningTokens,
	}
	model := answer.Model
	if model == "" {
		model = a.cfg.Model
	}
	u.Cost, u.Priced = llm.CostAt(model, answer.Usage, time.Now())
	return u
}
