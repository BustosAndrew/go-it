package models

import (
	"time"
)

// CallSession represents an active call
type CallSession struct {
	CallID       string       `json:"call_id"`
	AgentID      string       `json:"agent_id"`
	StartTime    time.Time    `json:"start_time"`
	CallerNumber string       `json:"caller_number"`
	LastActivity time.Time    `json:"last_activity"`
	Transcript   []Transcript `json:"transcript"`
	Summary      string       `json:"summary,omitempty"`
	Suggestions  []Suggestion `json:"suggestions,omitempty"`
	TicketID     string       `json:"ticket_id,omitempty"`
}

