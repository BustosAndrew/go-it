package models

import (
	"time"
)

// TicketStatus represents the current status of a ticket
type TicketStatus string

const (
	StatusOpen     TicketStatus = "open"
	StatusClosed   TicketStatus = "closed"
	StatusPending  TicketStatus = "pending"
)

// Ticket represents a support ticket created from a call
type Ticket struct {
	TicketID     string       `json:"ticket_id" firestore:"ticket_id"`
	CallID       string       `json:"call_id" firestore:"call_id"`
	AgentID      string       `json:"agent_id" firestore:"agent_id"`
	CallerNumber string       `json:"caller_number" firestore:"caller_number"`
	Status       TicketStatus `json:"status" firestore:"status"`
	CreatedAt    time.Time    `json:"created_at" firestore:"created_at"`
	UpdatedAt    time.Time    `json:"updated_at" firestore:"updated_at"`
	ClosedAt     *time.Time   `json:"closed_at,omitempty" firestore:"closed_at,omitempty"`
	Transcript   []Transcript `json:"transcript,omitempty" firestore:"transcript,omitempty"`
	Summary      string       `json:"summary,omitempty" firestore:"summary,omitempty"`
	Suggestions []Suggestion `json:"suggestions,omitempty"`
}
