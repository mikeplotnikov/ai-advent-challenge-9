// Package supportmcp serves a small support CRM — users, tickets and service
// incidents kept in one JSON file — through MCP.
//
// Every tool only reads. The file is read again on each call, so an operator can edit
// a ticket while the assistant is running and the next answer sees the change.
// get_ticket refuses a ticket of another user: the caller names the user, and the
// day-33 service always names the signed-in one.
package supportmcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	ServerName    = "day33-support-crm"
	ServerVersion = "1.0.0"
)

type CRM struct {
	// AsOf is the moment the data describes; the assistant treats it as "now".
	AsOf      string     `json:"as_of"`
	Users     []User     `json:"users"`
	Tickets   []Ticket   `json:"tickets"`
	Incidents []Incident `json:"incidents"`
}

type User struct {
	ID              string       `json:"id"`
	Name            string       `json:"name"`
	Email           string       `json:"email"`
	Plan            string       `json:"plan"`
	PlanActiveUntil string       `json:"plan_active_until"`
	PlanNote        string       `json:"plan_note,omitempty"`
	Payments        []Payment    `json:"payments"`
	Devices         []Device     `json:"devices"`
	LoginEvents     []LoginEvent `json:"login_events"`
	Courses         []Course     `json:"courses"`
	// LessonsSinceLastPayment counts lessons finished since the latest successful
	// charge — the figure the refund rule needs.
	LessonsSinceLastPayment int `json:"lessons_since_last_payment"`
}

type Payment struct {
	At        string `json:"at"`
	AmountRub int    `json:"amount_rub"`
	Status    string `json:"status"`
	Error     string `json:"error,omitempty"`
}

type Device struct {
	Name     string `json:"name"`
	Client   string `json:"client"`
	LastSeen string `json:"last_seen"`
}

type LoginEvent struct {
	At    string `json:"at"`
	Event string `json:"event"`
	Note  string `json:"note,omitempty"`
}

type Course struct {
	Title            string `json:"title"`
	LessonsTotal     int    `json:"lessons_total"`
	LessonsDone      int    `json:"lessons_done"`
	HomeworkTotal    int    `json:"homework_total"`
	HomeworkAccepted int    `json:"homework_accepted"`
	CompletedAt      string `json:"completed_at,omitempty"`
	Certificate      string `json:"certificate,omitempty"`
}

type Ticket struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	Subject   string    `json:"subject"`
	Status    string    `json:"status"`
	CreatedAt string    `json:"created_at"`
	Messages  []Message `json:"messages"`
}

type Message struct {
	From string `json:"from"`
	At   string `json:"at"`
	Text string `json:"text"`
}

type Incident struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Status    string `json:"status"`
	StartedAt string `json:"started_at"`
	ETA       string `json:"eta,omitempty"`
	Details   string `json:"details"`
}

type UserInput struct {
	UserID string `json:"user_id" jsonschema:"Идентификатор пользователя, например u1."`
}

type TicketInput struct {
	UserID   string `json:"user_id" jsonschema:"Идентификатор пользователя — владельца тикета."`
	TicketID string `json:"ticket_id" jsonschema:"Номер тикета, например T-101."`
}

type UserOutput struct {
	AsOf string `json:"as_of"`
	User User   `json:"user"`
}

type TicketSummary struct {
	ID        string `json:"id"`
	Subject   string `json:"subject"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
}

type TicketsOutput struct {
	UserID  string          `json:"user_id"`
	Tickets []TicketSummary `json:"tickets"`
}

type TicketOutput struct {
	Ticket Ticket `json:"ticket"`
}

type StatusOutput struct {
	AsOf      string     `json:"as_of"`
	Incidents []Incident `json:"incidents"`
}

func Load(path string) (CRM, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return CRM{}, err
	}
	var crm CRM
	if err := json.Unmarshal(raw, &crm); err != nil {
		return CRM{}, fmt.Errorf("CRM %s: %w", path, err)
	}
	return crm, nil
}

func (c CRM) User(id string) (User, error) {
	for _, u := range c.Users {
		if u.ID == strings.TrimSpace(id) {
			return u, nil
		}
	}
	return User{}, fmt.Errorf("пользователь %q не найден", id)
}

func (c CRM) TicketsOf(userID string) []TicketSummary {
	userID = strings.TrimSpace(userID)
	out := []TicketSummary{}
	for _, t := range c.Tickets {
		if t.UserID == userID {
			out = append(out, TicketSummary{ID: t.ID, Subject: t.Subject, Status: t.Status, CreatedAt: t.CreatedAt})
		}
	}
	return out
}

// Ticket returns a ticket only to its owner; for anyone else it does not exist.
func (c CRM) Ticket(userID, ticketID string) (Ticket, error) {
	for _, t := range c.Tickets {
		if t.ID == strings.TrimSpace(ticketID) && t.UserID == strings.TrimSpace(userID) {
			return t, nil
		}
	}
	return Ticket{}, fmt.Errorf("у пользователя %q нет тикета %q", userID, ticketID)
}

func NewServer(path string) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: ServerName, Version: ServerVersion}, nil)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_user",
		Description: "Профиль пользователя из CRM: тариф и срок подписки, платежи и ошибки оплаты, устройства, события входа, прогресс по курсам.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in UserInput) (*mcp.CallToolResult, UserOutput, error) {
		crm, err := Load(path)
		if err != nil {
			return nil, UserOutput{}, err
		}
		user, err := crm.User(in.UserID)
		return nil, UserOutput{AsOf: crm.AsOf, User: user}, err
	})
	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_tickets",
		Description: "Все тикеты пользователя: номер, тема, статус, дата создания.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in UserInput) (*mcp.CallToolResult, TicketsOutput, error) {
		crm, err := Load(path)
		if err != nil {
			return nil, TicketsOutput{}, err
		}
		if _, err := crm.User(in.UserID); err != nil {
			return nil, TicketsOutput{}, err
		}
		return nil, TicketsOutput{UserID: strings.TrimSpace(in.UserID), Tickets: crm.TicketsOf(in.UserID)}, nil
	})
	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_ticket",
		Description: "Тикет пользователя целиком: тема, статус и переписка.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in TicketInput) (*mcp.CallToolResult, TicketOutput, error) {
		crm, err := Load(path)
		if err != nil {
			return nil, TicketOutput{}, err
		}
		ticket, err := crm.Ticket(in.UserID, in.TicketID)
		return nil, TicketOutput{Ticket: ticket}, err
	})
	mcp.AddTool(server, &mcp.Tool{
		Name:        "service_status",
		Description: "Текущие сбои и инциденты сервиса Парта. Вызывай, когда проблема может быть на стороне сервиса (задержки, массовые ошибки).",
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, StatusOutput, error) {
		crm, err := Load(path)
		if err != nil {
			return nil, StatusOutput{}, err
		}
		incidents := crm.Incidents
		if incidents == nil {
			incidents = []Incident{}
		}
		return nil, StatusOutput{AsOf: crm.AsOf, Incidents: incidents}, nil
	})
	return server
}
