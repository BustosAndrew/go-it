package services

import (
	"context"
	"os"

	"cloud.google.com/go/firestore"
	firebase "firebase.google.com/go/v4"
	"google.golang.org/api/option"

	"github.com/joho/godotenv"
	"github.com/mitchellh/mapstructure"
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
		collectionName = "call_transcripts"
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
