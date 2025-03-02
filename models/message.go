package models

import "time"

// TranscriptUpdate is sent to frontend clients with the latest transcript data
type TranscriptUpdate struct {
	Type        string       `json:"type"`
	CallID      string       `json:"call_id"`
	AgentID     string       `json:"agent_id"`
	Transcript  []Transcript `json:"transcript"`
	StartTime   time.Time    `json:"start_time"`
	LastUpdated time.Time    `json:"last_updated"`
	IsActive    bool         `json:"is_active"`
}

// ConnectionResponse is sent when a client connects to the WebSocket
type ConnectionResponse struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Message string `json:"message"`
	CallID  string `json:"call_id,omitempty"`
}
