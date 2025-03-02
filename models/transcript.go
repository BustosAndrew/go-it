package models

import "time"

// CallTranscript represents a complete call record with metadata
type CallTranscript struct {
	CallID        string       `json:"call_id" firestore:"call_id"`
	AgentID       string       `json:"agent_id" firestore:"agent_id"`
	Transcript    []Transcript `json:"transcript" firestore:"transcript"`
	StartTime     time.Time    `json:"start_time" firestore:"start_time"`
	EndTime       time.Time    `json:"end_time" firestore:"end_time"`
	DurationSecs  int          `json:"duration_secs" firestore:"duration_secs"`
	CallerNumber  string       `json:"caller_number,omitempty" firestore:"caller_number,omitempty"`
}

// Transcript represents a single message in the conversation
type Transcript struct {
	Role    string    `json:"role" firestore:"role"`
	Content string    `json:"content" firestore:"content"`
	Time    time.Time `json:"time,omitempty" firestore:"time,omitempty"` // Add timestamp
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

// Suggestion represents a suggested solution for the caller's issue
type Suggestion struct {
	Title       string `json:"title" firestore:"title"`
	Description string `json:"description" firestore:"description"`
}
