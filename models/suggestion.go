package models

// Suggestion represents a suggested action or response
type Suggestion struct {
	Text     string `json:"text" firestore:"text"`
	Title   string `json:"title" firestore:"title"`
}