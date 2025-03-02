package models

import (
	"time"
)

// Transcript represents a single message in a conversation
type Transcript struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Suggestion represents an AI-generated suggestion for resolving an issue
type Suggestion struct {
	Title       string `json:"title"`
	Description string `json:"description"`
}

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

// TranscriptUpdate represents a message sent to the frontend about updates to a call
type TranscriptUpdate struct {
	Type        string       `json:"type"`
	CallID      string       `json:"call_id"`
	AgentID     string       `json:"agent_id"`
	Transcript  []Transcript `json:"transcript"`
	StartTime   time.Time    `json:"start_time"`
	LastUpdated time.Time    `json:"last_updated"`
	IsActive    bool         `json:"is_active"`
	TicketID    string       `json:"ticket_id,omitempty"`
	Summary     string       `json:"summary,omitempty"`
	Suggestions []Suggestion `json:"suggestions,omitempty"`
}

// ConnectionResponse represents a connection message
type ConnectionResponse struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Message string `json:"message"`
	CallID  string `json:"call_id"`
}
