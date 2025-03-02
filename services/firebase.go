package services

import (
	"context"
	"errors"
	"log"
	"os"
	"time"

	"cloud.google.com/go/firestore"
	firebase "firebase.google.com/go/v4"
	"github.com/google/uuid"
	"google.golang.org/api/option"

	"github.com/joho/godotenv"
	"github.com/mitchellh/mapstructure"

	"go-it/models"
)

// FirestoreClient wraps firebase client
type FirestoreClient struct {
	client *firestore.Client
	ctx    context.Context
}

var firestoreClient *FirestoreClient

// InitFirestore initializes the Firestore client
func InitFirestore() (*FirestoreClient, error) {
	if firestoreClient != nil {
		return firestoreClient, nil
	}

	_ = godotenv.Load()

	ctx := context.Background()
	
	var app *firebase.App
	var err error

	// Check if running in production with environment variable
	if credJSON := os.Getenv("FIREBASE_CREDENTIALS_JSON"); credJSON != "" {
		// Use credentials from environment variable
		opt := option.WithCredentialsJSON([]byte(credJSON))
		app, err = firebase.NewApp(ctx, nil, opt)
	} else if credFile := os.Getenv("FIREBASE_CREDENTIALS_FILE"); credFile != "" {
		// Use credentials from a file
		opt := option.WithCredentialsFile(credFile)
		app, err = firebase.NewApp(ctx, nil, opt)
	} else {
		// Try to use default credentials
		app, err = firebase.NewApp(ctx, nil)
	}

	if err != nil {
		return nil, err
	}

	client, err := app.Firestore(ctx)
	if err != nil {
		return nil, err
	}

	firestoreClient = &FirestoreClient{
		client: client,
		ctx:    ctx,
	}

	return firestoreClient, nil
}

// SaveCallTranscript saves a call transcript to Firestore
func (fc *FirestoreClient) SaveCallTranscript(callData interface{}) (string, error) {
	// Get the collection from environment or use default
	collectionName := os.Getenv("FIRESTORE_CALLS_COLLECTION")
	if collectionName == "" {
		collectionName = "calls"
	}

	// Convert data to map if needed
	var dataMap map[string]interface{}
	if err := mapstructure.Decode(callData, &dataMap); err != nil {
		return "", err
	}

	// Get the call ID from the map
	callID, ok := dataMap["call_id"].(string)
	if !ok || callID == "" {
		// Generate a document ID if call_id is not available
		ref := fc.client.Collection(collectionName).NewDoc()
		_, err := ref.Set(fc.ctx, dataMap)
		return ref.ID, err
	}

	// Use call ID as document ID
	ref := fc.client.Collection(collectionName).Doc(callID)
	_, err := ref.Set(fc.ctx, dataMap)
	return callID, err
}

// GetCallTranscript retrieves a call transcript from Firestore
func (fc *FirestoreClient) GetCallTranscript(callID string) (*models.CallTranscript, error) {
	if callID == "" {
		return nil, errors.New("call ID is required")
	}

	// Get the collection from environment or use default
	collectionName := os.Getenv("FIRESTORE_CALLS_COLLECTION")
	if collectionName == "" {
		collectionName = "calls"
	}

	// Get the document
	docRef := fc.client.Collection(collectionName).Doc(callID)
	docSnap, err := docRef.Get(fc.ctx)
	if err != nil {
		return nil, err
	}

	if !docSnap.Exists() {
		return nil, nil
	}

	// Convert to CallTranscript
	var transcript models.CallTranscript
	if err := docSnap.DataTo(&transcript); err != nil {
		return nil, err
	}

	return &transcript, nil
}

// GetAllCallTranscripts retrieves all call transcripts from Firestore
func (fc *FirestoreClient) GetAllCallTranscripts() ([]*models.CallTranscript, error) {
	// Get the collection from environment or use default
	collectionName := os.Getenv("FIRESTORE_CALLS_COLLECTION")
	if collectionName == "" {
		collectionName = "calls"
	}

	// Get all documents in the collection
	docs, err := fc.client.Collection(collectionName).Documents(fc.ctx).GetAll()
	if err != nil {
		return nil, err
	}

	var transcripts []*models.CallTranscript
	for _, doc := range docs {
		var transcript models.CallTranscript
		if err := doc.DataTo(&transcript); err != nil {
			// Log the error but continue processing other documents
			log.Printf("Error parsing document %s: %v", doc.Ref.ID, err)
			continue
		}
		transcripts = append(transcripts, &transcript)
	}

	return transcripts, nil
}

// CreateTicketForCall creates a new ticket for a call and returns the ticket ID
func (fc *FirestoreClient) CreateTicketForCall(callID, agentID, callerNumber string) (string, error) {
	// Get the tickets collection name from environment or use default
	collectionName := os.Getenv("FIRESTORE_TICKETS_COLLECTION")
	if collectionName == "" {
		collectionName = "tickets"
	}

	// Generate a new ticket ID if not provided
	ticketID := uuid.New().String()

	// Create the ticket
	ticket := models.Ticket{
		TicketID:     ticketID,
		CallID:       callID,
		AgentID:      agentID,
		CallerNumber: callerNumber,
		Status:       models.StatusOpen,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}

	// Save the ticket to Firestore
	_, err := fc.client.Collection(collectionName).Doc(ticketID).Set(fc.ctx, ticket)
	if err != nil {
		return "", err
	}

	return ticketID, nil
}

// UpdateTicketWithTranscript updates a ticket with the call transcript but keeps status as is
func (fc *FirestoreClient) UpdateTicketWithTranscript(ticketID string, transcript []models.Transcript) error {
	// Get the tickets collection name from environment or use default
	collectionName := os.Getenv("FIRESTORE_TICKETS_COLLECTION")
	if collectionName == "" {
		collectionName = "tickets"
	}

	// Get the ticket document reference
	ticketRef := fc.client.Collection(collectionName).Doc(ticketID)

	// Check if ticket exists
	doc, err := ticketRef.Get(fc.ctx)
	if err != nil {
		return err
	}

	if !doc.Exists() {
		return errors.New("ticket not found")
	}

	// Current time for update timestamp
	now := time.Now()

	// Update the ticket with transcript but keep status as is
	updates := []firestore.Update{
		{Path: "transcript", Value: transcript},
		{Path: "updated_at", Value: now},
	}

	_, err = ticketRef.Update(fc.ctx, updates)
	return err
}

// CloseTicket changes a ticket status to closed
func (fc *FirestoreClient) CloseTicket(ticketID string, resolution string) error {
	// Get the tickets collection name from environment or use default
	collectionName := os.Getenv("FIRESTORE_TICKETS_COLLECTION")
	if collectionName == "" {
		collectionName = "tickets"
	}

	// Get the ticket document reference
	ticketRef := fc.client.Collection(collectionName).Doc(ticketID)

	// Check if ticket exists
	doc, err := ticketRef.Get(fc.ctx)
	if err != nil {
		return err
	}

	if !doc.Exists() {
		return errors.New("ticket not found")
	}

	// Current time for closed timestamp
	now := time.Now()

	// Create a slice of firestore.Update
	updates := []firestore.Update{
		{Path: "status", Value: models.StatusClosed},
		{Path: "updated_at", Value: now},
		{Path: "closed_at", Value: now},
	}
	
	// Add resolution summary if provided
	if resolution != "" {
		updates = append(updates, firestore.Update{Path: "summary", Value: resolution})
	}

	_, err = ticketRef.Update(fc.ctx, updates)
	return err
}

// GetTicketByCallID retrieves a ticket by its associated call ID
func (fc *FirestoreClient) GetTicketByCallID(callID string) (*models.Ticket, error) {
	if callID == "" {
		return nil, errors.New("call ID is required")
	}

	// Get the tickets collection name from environment or use default
	collectionName := os.Getenv("FIRESTORE_TICKETS_COLLECTION")
	if collectionName == "" {
		collectionName = "tickets"
	}

	// Query for tickets with the matching call ID
	query := fc.client.Collection(collectionName).Where("call_id", "==", callID).Limit(1)
	docs, err := query.Documents(fc.ctx).GetAll()
	if err != nil {
		return nil, err
	}

	if len(docs) == 0 {
		return nil, nil
	}

	// Convert to Ticket
	var ticket models.Ticket
	if err := docs[0].DataTo(&ticket); err != nil {
		return nil, err
	}

	return &ticket, nil
}

// GetAllTickets retrieves all tickets from Firestore
func (fc *FirestoreClient) GetAllTickets() ([]*models.Ticket, error) {
	// Get the collection from environment or use default
	collectionName := os.Getenv("FIRESTORE_TICKETS_COLLECTION")
	if collectionName == "" {
		collectionName = "tickets"
	}

	// Get all documents in the collection
	docs, err := fc.client.Collection(collectionName).Documents(fc.ctx).GetAll()
	if err != nil {
		return nil, err
	}

	var tickets []*models.Ticket
	for _, doc := range docs {
		var ticket models.Ticket
		if err := doc.DataTo(&ticket); err != nil {
			// Log the error but continue processing other documents
			log.Printf("Error parsing ticket document %s: %v", doc.Ref.ID, err)
			continue
		}
		tickets = append(tickets, &ticket)
	}

	return tickets, nil
}

// GetTicketByID retrieves a ticket by its ID
func (fc *FirestoreClient) GetTicketByID(ticketID string) (*models.Ticket, error) {
	if ticketID == "" {
		return nil, errors.New("ticket ID is required")
	}

	// Get the collection from environment or use default
	collectionName := os.Getenv("FIRESTORE_TICKETS_COLLECTION")
	if collectionName == "" {
		collectionName = "tickets"
	}

	// Get the document
	docRef := fc.client.Collection(collectionName).Doc(ticketID)
	docSnap, err := docRef.Get(fc.ctx)
	if err != nil {
		return nil, err
	}

	if !docSnap.Exists() {
		return nil, errors.New("ticket not found")
	}

	// Convert to Ticket
	var ticket models.Ticket
	if err := docSnap.DataTo(&ticket); err != nil {
		return nil, err
	}

	return &ticket, nil
}

// Close closes the Firestore client
func (fc *FirestoreClient) Close() error {
	if fc.client != nil {
		return fc.client.Close()
	}
	return nil
}

// GetFirestoreClient returns the singleton instance of FirestoreClient
func GetFirestoreClient() (*FirestoreClient, error) {
	if firestoreClient == nil {
		return InitFirestore()
	}
	return firestoreClient, nil
}
