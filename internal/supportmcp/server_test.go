package supportmcp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/mcpclient"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const fixture = `{
  "as_of": "2026-10-16T14:00:00+03:00",
  "users": [
    {"id": "u1", "name": "Анна", "plan": "Про", "login_events": [{"at": "2026-10-16T13:47:00+03:00", "event": "locked", "note": "заблокировано до 14:17"}]},
    {"id": "u2", "name": "Борис", "plan": "Старт"}
  ],
  "tickets": [
    {"id": "T-1", "user_id": "u1", "subject": "Не могу войти", "status": "open"},
    {"id": "T-2", "user_id": "u2", "subject": "Замок на уроках", "status": "open"},
    {"id": "T-3", "user_id": "u2", "subject": "Возврат", "status": "closed"}
  ],
  "incidents": [{"id": "INC-1", "title": "Задержка сертификатов", "status": "investigating"}]
}`

func TestToolsReadTheCRMOverMCP(t *testing.T) {
	path := writeCRM(t, fixture)
	session, closeFn := open(t, path)
	defer closeFn()

	listed, err := session.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range listed.Tools {
		names = append(names, tool.Name)
	}
	if got := strings.Join(names, ","); got != "get_ticket,get_user,list_tickets,service_status" {
		t.Fatalf("tools: %s", got)
	}

	user := ok(t, session, "get_user", map[string]any{"user_id": "u1"})
	if !strings.Contains(user, `"as_of":"2026-10-16T14:00:00+03:00"`) || !strings.Contains(user, "заблокировано до 14:17") {
		t.Fatalf("get_user: %s", user)
	}
	tickets := ok(t, session, "list_tickets", map[string]any{"user_id": "u2"})
	if !strings.Contains(tickets, `"T-2"`) || !strings.Contains(tickets, `"T-3"`) || strings.Contains(tickets, `"T-1"`) {
		t.Fatalf("list_tickets u2: %s", tickets)
	}
	if padded := ok(t, session, "list_tickets", map[string]any{"user_id": " u2 "}); !strings.Contains(padded, `"user_id":"u2"`) || !strings.Contains(padded, `"T-2"`) {
		t.Fatalf("list_tickets with spaces around the id: %s", padded)
	}
	ticket := ok(t, session, "get_ticket", map[string]any{"user_id": "u2", "ticket_id": "T-2"})
	if !strings.Contains(ticket, "Замок на уроках") {
		t.Fatalf("get_ticket: %s", ticket)
	}
	status := ok(t, session, "service_status", map[string]any{})
	if !strings.Contains(status, "INC-1") {
		t.Fatalf("service_status: %s", status)
	}
}

func TestATicketIsVisibleOnlyToItsOwner(t *testing.T) {
	path := writeCRM(t, fixture)
	session, closeFn := open(t, path)
	defer closeFn()

	result := call(t, session, "get_ticket", map[string]any{"user_id": "u1", "ticket_id": "T-2"})
	text := mcpclient.ToolText(result)
	if !result.IsError || strings.Contains(text, "Замок на уроках") {
		t.Fatalf("foreign ticket leaked: isError=%v %s", result.IsError, text)
	}
	for _, args := range []map[string]any{{"user_id": "u9"}, {"user_id": ""}} {
		if result := call(t, session, "get_user", args); !result.IsError {
			t.Fatalf("get_user %v: expected an error, got %s", args, mcpclient.ToolText(result))
		}
		if result := call(t, session, "list_tickets", args); !result.IsError {
			t.Fatalf("list_tickets %v: expected an error, got %s", args, mcpclient.ToolText(result))
		}
	}
}

func TestEditsToTheFileReachTheNextCall(t *testing.T) {
	path := writeCRM(t, fixture)
	session, closeFn := open(t, path)
	defer closeFn()

	if text := ok(t, session, "service_status", map[string]any{}); !strings.Contains(text, "INC-1") {
		t.Fatalf("before edit: %s", text)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(fixture, `"incidents": [{"id": "INC-1", "title": "Задержка сертификатов", "status": "investigating"}]`, `"incidents": []`, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if text := ok(t, session, "service_status", map[string]any{}); strings.Contains(text, "INC-1") || !strings.Contains(text, `"incidents":[]`) {
		t.Fatalf("after edit: %s", text)
	}
}

func TestTheDay33DatasetLoadsAndEveryTicketHasAnOwner(t *testing.T) {
	crm, err := Load(filepath.Join("..", "..", "day-33", "data", "crm.json"))
	if err != nil {
		t.Fatal(err)
	}
	if crm.AsOf == "" || len(crm.Users) == 0 || len(crm.Tickets) == 0 {
		t.Fatalf("dataset is empty: %+v", crm)
	}
	for _, ticket := range crm.Tickets {
		if _, err := crm.User(ticket.UserID); err != nil {
			t.Fatalf("ticket %s: %v", ticket.ID, err)
		}
	}
}

func writeCRM(t *testing.T, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "crm.json")
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func open(t *testing.T, path string) (*mcpclient.Session, func()) {
	t.Helper()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := NewServer(path).Connect(context.Background(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	session, err := mcpclient.NewNamed("crm-test", "1").Open(context.Background(), mcpclient.Transport{MCP: clientTransport})
	if err != nil {
		t.Fatal(err)
	}
	return session, func() { _ = session.Close(); _ = serverSession.Close() }
}

func call(t *testing.T, session *mcpclient.Session, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	result, err := session.CallTool(context.Background(), name, args)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func ok(t *testing.T, session *mcpclient.Session, name string, args map[string]any) string {
	t.Helper()
	result := call(t, session, name, args)
	text := mcpclient.ToolText(result)
	if result.IsError {
		t.Fatalf("%s %v: %s", name, args, text)
	}
	return text
}
