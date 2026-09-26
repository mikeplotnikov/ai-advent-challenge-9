package mcprouter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/mcpclient"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/toolagent"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	MaxCalls            = 16
	DefaultCallTimeout  = 45 * time.Second
	DefaultStartTimeout = 60 * time.Second
	LimitText           = "лимит вызовов инструментов на запрос исчерпан (16)"
)

var mergedNameRE = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)

type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type Server struct {
	Alias      string     `json:"alias"`
	ServerInfo ServerInfo `json:"serverInfo"`
	Protocol   string     `json:"protocol,omitempty"`
	Transport  string     `json:"transport"`
	Command    string     `json:"command,omitempty"`
	Args       []string   `json:"args,omitempty"`
	ToolNames  []string   `json:"tools"`
}

type JournalEntry struct {
	Seq          int           `json:"seq"`
	Name         string        `json:"name"`
	Alias        string        `json:"alias"`
	ServerInfo   ServerInfo    `json:"serverInfo"`
	Transport    string        `json:"transport"`
	OriginalName string        `json:"originalName"`
	Outcome      string        `json:"outcome"`
	Duration     time.Duration `json:"duration"`
	Text         string        `json:"text,omitempty"`
}

type Session interface {
	toolagent.Session
	Close() error
}

type connection struct {
	server   Server
	session  Session
	cmd      *exec.Cmd
	dead     bool
	deadText string
	closed   bool
}

type route struct {
	conn *connection
	tool string
}

type Options struct {
	StartTimeout time.Duration
	CallTimeout  time.Duration
	Stderr       *os.File
}

type Router struct {
	mu          sync.Mutex
	connections []*connection
	routes      map[string]route
	tools       []*mcp.Tool
	journal     []JournalEntry
	seq         int
	runCalls    int
	callTimeout time.Duration
}

func Open(ctx context.Context, registry Registry, options Options) (*Router, error) {
	startTimeout := options.StartTimeout
	if startTimeout <= 0 {
		startTimeout = DefaultStartTimeout
	}
	callTimeout := options.CallTimeout
	if callTimeout <= 0 {
		callTimeout = DefaultCallTimeout
	}
	router := &Router{routes: map[string]route{}, callTimeout: callTimeout}
	for _, entry := range registry.Servers {
		cmd := exec.Command(entry.Command, entry.Args...)
		cmd.Env = childEnv(os.Environ(), entry.Env)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if options.Stderr != nil {
			cmd.Stderr = options.Stderr
		}
		transport := mcpclient.Transport{
			MCP:         &mcp.CommandTransport{Command: cmd, TerminateDuration: 100 * time.Millisecond},
			Description: "stdio", Command: commandString(entry),
		}
		startupCtx, cancel := context.WithTimeout(ctx, startTimeout)
		opened, err := mcpclient.NewNamed("ai-advent-day-20-agent", "1").Open(startupCtx, transport)
		if err != nil {
			cancel()
			killProcessGroup(cmd)
			router.Close()
			return nil, fmt.Errorf("сервер %q: запуск: %w", entry.Alias, err)
		}
		conn := &connection{server: Server{
			Alias: entry.Alias, ServerInfo: ServerInfo{Name: opened.ServerName, Version: opened.ServerVersion},
			Protocol: opened.ProtocolVersion, Transport: "stdio", Command: entry.Command, Args: append([]string(nil), entry.Args...),
		}, session: opened, cmd: cmd}
		router.connections = append(router.connections, conn)
		listed, listErr := opened.ListTools(startupCtx)
		cancel()
		if listErr != nil {
			router.Close()
			return nil, fmt.Errorf("сервер %q: tools/list: %w", entry.Alias, listErr)
		}
		if err := router.addTools(conn, listed); err != nil {
			router.Close()
			return nil, err
		}
	}
	return router, nil
}

// NewForSessions builds a router over already initialized sessions, for deterministic tests and dumps.
func NewForSessions(items []SessionDescriptor, callTimeout time.Duration) (*Router, error) {
	if callTimeout <= 0 {
		callTimeout = DefaultCallTimeout
	}
	router := &Router{routes: map[string]route{}, callTimeout: callTimeout}
	for _, item := range items {
		conn := &connection{server: Server{Alias: item.Alias, ServerInfo: item.ServerInfo,
			Protocol: item.Protocol, Transport: item.Transport}, session: item.Session}
		router.connections = append(router.connections, conn)
		listed, err := item.Session.ListTools(context.Background())
		if err != nil {
			router.Close()
			return nil, fmt.Errorf("сервер %q: tools/list: %w", item.Alias, err)
		}
		if err := router.addTools(conn, listed); err != nil {
			router.Close()
			return nil, err
		}
	}
	return router, nil
}

type SessionDescriptor struct {
	Alias, Protocol, Transport string
	ServerInfo                 ServerInfo
	Session                    Session
}

func (r *Router) addTools(conn *connection, listed *mcp.ListToolsResult) error {
	for _, tool := range listed.Tools {
		full := conn.server.Alias + "__" + tool.Name
		if !mergedNameRE.MatchString(full) {
			return fmt.Errorf("сервер %q: итоговое имя инструмента %q нарушает правило DeepSeek", conn.server.Alias, full)
		}
		if _, exists := r.routes[full]; exists {
			return fmt.Errorf("сервер %q: итоговое имя инструмента %q повторяется", conn.server.Alias, full)
		}
		copyTool := *tool
		copyTool.Name = full
		r.tools = append(r.tools, &copyTool)
		r.routes[full] = route{conn: conn, tool: tool.Name}
		conn.server.ToolNames = append(conn.server.ToolNames, full)
	}
	return nil
}

func (r *Router) ListTools(context.Context) (*mcp.ListToolsResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return &mcp.ListToolsResult{Tools: append([]*mcp.Tool(nil), r.tools...)}, nil
}

func (r *Router) CallTool(ctx context.Context, name string, arguments map[string]any) (*mcp.CallToolResult, error) {
	started := time.Now()
	r.mu.Lock()
	r.seq++
	r.runCalls++
	seq, count := r.seq, r.runCalls
	route, known := r.routes[name]
	entry := JournalEntry{Seq: seq, Name: name, OriginalName: strings.TrimPrefix(name, strings.SplitN(name, "__", 2)[0]+"__")}
	if known {
		entry.Alias, entry.ServerInfo, entry.Transport, entry.OriginalName = route.conn.server.Alias,
			route.conn.server.ServerInfo, route.conn.server.Transport, route.tool
	}
	if count > MaxCalls {
		entry.Outcome, entry.Text, entry.Duration = "limit", LimitText, time.Since(started)
		r.journal = append(r.journal, entry)
		r.mu.Unlock()
		return errorResult(LimitText), nil
	}
	if !known {
		text := fmt.Sprintf("инструмент %q не зарегистрирован в маршрутизаторе", name)
		entry.Outcome, entry.Text, entry.Duration = "rpc_error", text, time.Since(started)
		r.journal = append(r.journal, entry)
		r.mu.Unlock()
		return errorResult(text), nil
	}
	if route.conn.dead {
		entry.Outcome, entry.Text, entry.Duration = "dead", route.conn.deadText, time.Since(started)
		r.journal = append(r.journal, entry)
		r.mu.Unlock()
		return errorResult(route.conn.deadText), nil
	}
	r.mu.Unlock()

	callCtx, cancel := context.WithTimeout(ctx, r.callTimeout)
	result, err := route.conn.session.CallTool(callCtx, route.tool, arguments)
	cancel()
	entry.Duration = time.Since(started)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err == nil {
		entry.Outcome = "ok"
		if result != nil && result.IsError {
			entry.Outcome = "tool_error"
		}
		entry.Text = mcpclient.ToolText(result)
		r.appendJournal(entry)
		return result, nil
	}
	var rpcErr *jsonrpc.Error
	if errors.As(err, &rpcErr) {
		entry.Outcome, entry.Text = "rpc_error", err.Error()
		r.appendJournal(entry)
		return errorResult(err.Error()), nil
	}
	if errors.Is(err, context.DeadlineExceeded) {
		text := fmt.Sprintf("сервер %s не ответил за %s", route.conn.server.Alias, durationText(r.callTimeout))
		entry.Outcome, entry.Text = "timeout", text
		r.markDead(route.conn, text)
		r.appendJournal(entry)
		return errorResult(text), nil
	}
	text := fmt.Sprintf("сервер %s недоступен: %s", route.conn.server.Alias, err)
	entry.Outcome, entry.Text = "unavailable", text
	r.markDead(route.conn, text)
	r.appendJournal(entry)
	return errorResult(text), nil
}

func (r *Router) ResetRun() {
	r.mu.Lock()
	r.runCalls = 0
	r.mu.Unlock()
}

func (r *Router) Journal() []JournalEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]JournalEntry(nil), r.journal...)
}

func (r *Router) Servers() []Server {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]Server, 0, len(r.connections))
	for _, conn := range r.connections {
		server := conn.server
		server.Args = append([]string(nil), server.Args...)
		server.ToolNames = append([]string(nil), server.ToolNames...)
		result = append(result, server)
	}
	return result
}

// ProcessIDs returns the direct child PID for every process-backed server.
// It is used by orchestration tests to prove registry reuse and cleanup.
func (r *Router) ProcessIDs() map[string]int {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := map[string]int{}
	for _, conn := range r.connections {
		if conn.cmd != nil && conn.cmd.Process != nil {
			result[conn.server.Alias] = conn.cmd.Process.Pid
		}
	}
	return result
}

func (r *Router) Close() error {
	r.mu.Lock()
	connections := append([]*connection(nil), r.connections...)
	r.mu.Unlock()
	var errs []error
	for i := len(connections) - 1; i >= 0; i-- {
		conn := connections[i]
		r.mu.Lock()
		if conn.closed {
			r.mu.Unlock()
			continue
		}
		conn.closed = true
		r.mu.Unlock()
		if err := conn.session.Close(); err != nil {
			errs = append(errs, err)
		}
		killProcessGroup(conn.cmd)
	}
	return errors.Join(errs...)
}

func (r *Router) markDead(conn *connection, text string) {
	r.mu.Lock()
	conn.dead, conn.deadText = true, text
	shouldClose := conn.cmd != nil && !conn.closed
	if shouldClose {
		conn.closed = true
	}
	r.mu.Unlock()
	if shouldClose {
		_ = conn.session.Close()
		killProcessGroup(conn.cmd)
	}
}

func (r *Router) appendJournal(entry JournalEntry) {
	r.mu.Lock()
	r.journal = append(r.journal, entry)
	r.mu.Unlock()
}

func errorResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}

func childEnv(base []string, extra map[string]string) []string {
	values := map[string]string{}
	for _, item := range base {
		name, value, ok := strings.Cut(item, "=")
		if ok && !strings.HasPrefix(name, "DEEPSEEK_API_KEY") {
			values[name] = value
		}
	}
	for name, value := range extra {
		values[name] = value
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]string, 0, len(names))
	for _, name := range names {
		result = append(result, name+"="+values[name])
	}
	return result
}

func commandString(entry Entry) string {
	encoded, _ := json.Marshal(append([]string{entry.Command}, entry.Args...))
	return string(encoded)
}

func killProcessGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}

func durationText(value time.Duration) string {
	if value == DefaultCallTimeout {
		return "45 с"
	}
	return value.String()
}
