package models

import (
	"time"
)

// Transcript represents a single message in the conversation
type Transcript struct {
	Role    string    `json:"role" firestore:"role"`
	Content string    `json:"content" firestore:"content"`
	Time    time.Time `json:"time,omitempty" firestore:"time,omitempty"`
}

// ConnectionResponse represents a response to a frontend connection request
type ConnectionResponse struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Message string `json:"message"`
	CallID  string `json:"call_id"`
}

// TranscriptUpdate represents a transcript update sent to the frontend
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
