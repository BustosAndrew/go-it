package models

import "time"

// Transcript represents a single message in a conversation
type Transcript struct {
	Role    string `json:"role" firestore:"role"`
	Content string `json:"content" firestore:"content"`
}

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
