package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/joho/godotenv"

	"github.com/sashabaranov/go-openai"
	"github.com/twilio/twilio-go/twiml"

	"go-it/models"
	"go-it/services"
)

// Global map to store active call transcripts
var (
	activeCallsMutex sync.RWMutex
	activeCalls      = make(map[string]*CallSession)
)

// CallSession stores session information for an active call
type CallSession struct {
	CallID        string
	AgentID       string
	StartTime     time.Time
	Transcript    []models.Transcript
	CallerNumber  string
	LastActivity  time.Time
	Summary       string
	Suggestions   []models.Suggestion
	TicketID      string
	mutex         sync.Mutex
}

// TicketCloseRequest struct for the close ticket endpoint
type TicketCloseRequest struct {
	Resolution string `json:"resolution"`
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8081" // Default port if not set
	}
    
	err := godotenv.Load()
	if err != nil {
		log.Println("Warning: cannot retrieve env file, using environment variables")
	}
   
	// Initialize Firebase
	firestoreClient, err := services.InitFirestore()
	if err != nil {
		log.Printf("Warning: Failed to initialize Firestore: %v", err)
	} else {
		defer firestoreClient.Close()
		log.Println("Firestore initialized successfully")
	}
   
	// Initialize WebSocket hub - removed variable assignment
	services.GetWebSocketHub() // Just initialize the hub without storing the reference
	log.Println("WebSocket hub initialized")
   
	// Start a background goroutine to clean up inactive calls
	go cleanupInactiveCalls()
   
	// gin.SetMode(gin.ReleaseMode)
	app := gin.Default()
	app.Use(cors.New(cors.Config{
		AllowAllOrigins: true,
		AllowMethods:     []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowHeaders:     []string{"Origin", "Content-Type", "Accept", "Authorization"},
		AllowCredentials: true,
		MaxAge:           12 * time.Hour,
	}))
    
	// API endpoints
	app.Any("/llm-websocket/:call_id", Retellwshandler)
	
	// New endpoint for direct session creation (independent of Twilio)
	app.POST("/api/calls/create", CreateCallSessionHandler)
    
	// New endpoint for SIP integration documentation
	app.GET("/api/telephony/info", TelephonyIntegrationInfoHandler)
    
	// New endpoint for frontend WebSocket connections
	app.GET("/frontend-ws/:call_id", FrontendWebSocketHandler)
    
	// Endpoint to list active calls
	app.GET("/api/calls/active", GetActiveCallsHandler)
    
	// Add new endpoint for tickets
	app.GET("/api/tickets", GetAllTicketsHandler)
	app.GET("/api/tickets/:ticket_id", GetTicketByIDHandler)
	app.POST("/api/tickets/:ticket_id/close", CloseTicketHandler)
    
	app.Run("localhost:" + port)
}

// CreateCallSessionRequest defines the request format for creating a new call session
type CreateCallSessionRequest struct {
	AgentID       string `json:"agent_id" binding:"required"`
	CallerNumber  string `json:"caller_number"`
	CallerName    string `json:"caller_name,omitempty"`
}

// CreateCallSessionResponse defines the response format for the create call endpoint
type CreateCallSessionResponse struct {
	CallID        string    `json:"call_id"`
	AgentID       string    `json:"agent_id"`
	TicketID      string    `json:"ticket_id,omitempty"`
	StartTime     time.Time `json:"start_time"`
	WebSocketURL  string    `json:"websocket_url"`
}

// TelephonyIntegrationInfoHandler provides information about Retell's SIP integration
func TelephonyIntegrationInfoHandler(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status": "success",
		"message": "Retell now uses SIP for telephony integration",
		"integration_details": map[string]interface{}{
			"method": "SIP",
			"documentation_url": "https://docs.retellai.com/tutorials/custom-telephony-integration-guide",
			"note": "The Twilio webhook handler is kept for backward compatibility but is deprecated",
		},
		"websocket_endpoint": "/llm-websocket/:call_id",
		"frontend_websocket": "/frontend-ws/:call_id",
		"direct_session_creation": "/api/calls/create",
	})
}

// CreateCallSessionHandler creates a new call session without requiring Twilio
func CreateCallSessionHandler(c *gin.Context) {
	var request CreateCallSessionRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"status": "error",
			"error":  "Invalid request format: " + err.Error(),
		})
		return
	}
	
	// Generate a call ID
	callID := "call_" + uuid.New().String()
	
	// Create session and ticket using our extracted helper function
	ticketID, session := createCallSession(callID, request.AgentID, request.CallerNumber)
	
	// Create the response
	response := CreateCallSessionResponse{
		CallID:       callID,
		AgentID:      request.AgentID,
		TicketID:     ticketID,
		StartTime:    session.StartTime,
		WebSocketURL: "wss://" + c.Request.Host + "/llm-websocket/" + callID,
	}
	
	c.JSON(http.StatusCreated, gin.H{
		"status": "success",
		"call":   response,
	})
}

// createCallSession is an extracted helper function to create a call session and ticket
// This allows the functionality to be used by both the Twilio webhook and direct API
func createCallSession(callID, agentID, callerNumber string) (string, *CallSession) {
	var ticketID string
	
	// Create a ticket for the call
	firestoreClient, err := services.GetFirestoreClient()
	if err == nil {
		ticketID, err = firestoreClient.CreateTicketForCall(callID, agentID, callerNumber)
		if err != nil {
			log.Printf("Error creating ticket: %v", err)
		} else {
			log.Printf("Created ticket %s for call %s", ticketID, callID)
		}
	} else {
		log.Printf("Error getting Firestore client: %v", err)
	}
	
	// Create a new call session
	session := &CallSession{
		CallID:       callID,
		AgentID:      agentID,
		StartTime:    time.Now(),
		CallerNumber: callerNumber,
		LastActivity: time.Now(),
		Transcript:   []models.Transcript{},
		Summary:      "",
		Suggestions:  []models.Suggestion{},
		TicketID:     ticketID,
	}
	
	// Store the session
	activeCallsMutex.Lock()
	activeCalls[callID] = session
	activeCallsMutex.Unlock()
	
	return ticketID, session
}

// GetActiveCallsHandler returns a list of all active calls
func GetActiveCallsHandler(c *gin.Context) {
	activeCallsMutex.RLock()
	defer activeCallsMutex.RUnlock()
    
	// Changed variable name from 'activeCalls' to 'activeCallsList' to avoid shadowing
	activeCallsList := make([]map[string]interface{}, 0, len(activeCalls))
    
	for _, session := range activeCalls {
		callData := map[string]interface{}{
			"call_id":       session.CallID,
			"agent_id":      session.AgentID,
			"start_time":    session.StartTime,
			"caller_number": session.CallerNumber,
			"last_activity": session.LastActivity,
			"duration":      time.Since(session.StartTime).Seconds(),
			"ticket_id":     session.TicketID,
			"summary":       session.Summary,
			"suggestions":   session.Suggestions,
		}
		activeCallsList = append(activeCallsList, callData)
	}
    
	c.JSON(http.StatusOK, gin.H{
		"status": "success",
		"calls":  activeCallsList,
	})
}

// GetAllTicketsHandler returns a list of all tickets
func GetAllTicketsHandler(c *gin.Context) {
	// Get the Firestore client
	firestoreClient, err := services.GetFirestoreClient()
	if err != nil {
		log.Printf("Error getting Firestore client: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"status": "error",
			"error":  "Failed to connect to database",
		})
		return
	}
    
	// Retrieve all tickets
	tickets, err := firestoreClient.GetAllTickets()
	if err != nil {
		log.Printf("Error retrieving tickets: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"status": "error",
			"error":  "Failed to retrieve tickets",
		})
		return
	}
    
	c.JSON(http.StatusOK, gin.H{
		"status": "success",
		"tickets": tickets,
	})
}

// GetTicketByIDHandler returns a specific ticket by ID
func GetTicketByIDHandler(c *gin.Context) {
	ticketID := c.Param("ticket_id")
    
	// Get the Firestore client
	firestoreClient, err := services.GetFirestoreClient()
	if err != nil {
		log.Printf("Error getting Firestore client: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"status": "error",
			"error":  "Failed to connect to database",
		})
		return
	}
    
	// Get the ticket using the new method
	ticket, err := firestoreClient.GetTicketByID(ticketID)
	if err != nil {
		if err.Error() == "ticket not found" {
			c.JSON(http.StatusNotFound, gin.H{
				"status": "error",
				"error":  "Ticket not found",
			})
		} else {
			log.Printf("Error retrieving ticket: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{
				"status": "error",
				"error":  "Failed to retrieve ticket",
			})
		}
		return
	}
    
	c.JSON(http.StatusOK, gin.H{
		"status": "success",
		"ticket": ticket,
	})
}

// CloseTicketHandler handles requests to manually close a ticket
func CloseTicketHandler(c *gin.Context) {
	ticketID := c.Param("ticket_id")
    
	var request TicketCloseRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"status": "error",
			"error":  "Invalid request format",
		})
		return
	}
    
	// Get the Firestore client
	firestoreClient, err := services.GetFirestoreClient()
	if err != nil {
		log.Printf("Error getting Firestore client: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"status": "error",
			"error":  "Failed to connect to database",
		})
		return
	}
    
	// Close the ticket
	err = firestoreClient.CloseTicket(ticketID, request.Resolution)
	if err != nil {
		log.Printf("Error closing ticket: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"status": "error",
			"error":  "Failed to close ticket",
		})
		return
	}
    
	c.JSON(http.StatusOK, gin.H{
		"status":  "success",
		"message": "Ticket closed successfully",
	})
}

// FrontendWebSocketHandler handles WebSocket connections from the frontend
func FrontendWebSocketHandler(c *gin.Context) {
	callID := c.Param("call_id")

  if c.Request.Method == "OPTIONS" {
    c.Status(http.StatusOK)
    return
}
	// Configure the upgrader
	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			return true // Allow all origins
		},
	}
    
	// Upgrade the HTTP connection to a WebSocket connection
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Printf("Error upgrading to WebSocket: %v", err)
		return
	}
    
	// Create a unique client ID
	clientID := uuid.New().String()
    
	// Create a client
	client := &services.Client{
		ID:     clientID,
		CallID: callID,
		Conn:   conn,
		Send:   make(chan []byte, 256),
		Hub:    services.GetWebSocketHub(),
	}
    
	// Register the client
	client.Hub.Register <- client
    
	// Start the write pump in a goroutine
	go client.WritePump()
    
	// Send initial connection success message
	connectionMsg := models.ConnectionResponse{
		Type:    "connection",
		Status:  "success",
		Message: "Connected to call stream",
		CallID:  callID,
	}
    
	// Send the initial connection message
	client.Hub.Broadcast(callID, connectionMsg)
    
	// Send current transcript if available
	activeCallsMutex.RLock()
	session, exists := activeCalls[callID]
	activeCallsMutex.RUnlock()
    
	if exists {
		// Send the current transcript
		session.mutex.Lock()
		update := models.TranscriptUpdate{
			Type:        "transcript_update",
			CallID:      callID,
			AgentID:     session.AgentID,
			Transcript:  session.Transcript,
			StartTime:   session.StartTime,
			LastUpdated: session.LastActivity,
			IsActive:    true,
			TicketID:    session.TicketID,
			Summary:     session.Summary,
			Suggestions: session.Suggestions,
		}
		session.mutex.Unlock()
        
		client.Hub.Broadcast(callID, update)
	} else {
		// Try to get from Firestore if call is completed
		firestoreClient, err := services.GetFirestoreClient()
		if err != nil {
			log.Printf("Error getting Firestore client: %v", err)
		} else {
			ticket, err := firestoreClient.GetTicketByCallID(callID)
			if err != nil {
				log.Printf("Error retrieving ticket for call %s: %v", callID, err)
			} else if ticket != nil {
				// Send the ticket data
				update := models.TranscriptUpdate{
					Type:        "transcript_update",
					CallID:      ticket.CallID,
					AgentID:     ticket.AgentID,
					Transcript:  ticket.Transcript,
					StartTime:   ticket.CreatedAt,
					LastUpdated: ticket.UpdatedAt,
					IsActive:    false,
					TicketID:    ticket.TicketID,
					Summary:     ticket.Summary,
					Suggestions: ticket.Suggestions,
				}
                
				client.Hub.Broadcast(callID, update)
			} else {
				// No ticket found
				client.Hub.Broadcast(callID, models.ConnectionResponse{
					Type:    "error",
					Status:  "error",
					Message: "No data found for this call ID",
					CallID:  callID,
				})
			}
		}
	}
    
	// Keep the connection alive until client disconnects
	for {
		_, _, err := conn.ReadMessage()
		if err != nil {
			client.Hub.Unregister <- client
			break
		}
	}
}

// broadcastTranscriptUpdate sends a transcript update to all connected frontend clients
func broadcastTranscriptUpdate(callID string) {
	activeCallsMutex.RLock()
	session, exists := activeCalls[callID]
	activeCallsMutex.RUnlock()
    
	if !exists {
		return
	}
    
	// Create update message
	session.mutex.Lock()
	update := models.TranscriptUpdate{
		Type:        "transcript_update",
		CallID:      callID,
		AgentID:     session.AgentID,
		Transcript:  session.Transcript,
		StartTime:   session.StartTime,
		LastUpdated: time.Now(),
		IsActive:    true,
		TicketID:    session.TicketID,
		Summary:     session.Summary,
		Suggestions: session.Suggestions,
	}
	session.mutex.Unlock()
    
	// Broadcast to all clients subscribed to this call
	services.GetWebSocketHub().Broadcast(callID, update)
}

func GetRetellAISecretKey() string {
	return os.Getenv("RETELL_API_KEY")
}

func GetOpenAISecretKey() string {
	return os.Getenv("OPENAI_API_KEY")
}

type RegisterCallRequest struct {
	AgentID                string `json:"agent_id"`
	AudioEncoding          string `json:"audio_encoding"`
	AudioWebsocketProtocol string `json:"audio_websocket_protocol"`
	SampleRate             int    `json:"sample_rate"`
}

type RegisterCallResponse struct {
	AgentID                string `json:"agent_id"`
	AudioEncoding          string `json:"audio_encoding"`
	AudioWebsocketProtocol string `json:"audio_websocket_protocol"`
	CallID                 string `json:"call_id"`
	CallStatus             string `json:"call_status"`
	SampleRate             int    `json:"sample_rate"`
	StartTimestamp         int    `json:"start_timestamp"`
}

// handleCallFailure handles a failed Retell API call
func handleCallFailure(c *gin.Context, agent_id string, callerNumber string) {
	// Generate a fallback call ID when Retell API fails
	fallbackCallID := "fallback_" + uuid.New().String()
	log.Printf("Using fallback call ID: %s", fallbackCallID)
	
	// Create a fallback session without relying on Retell
	session := &CallSession{
		CallID:       fallbackCallID,
		AgentID:      agent_id,
		StartTime:    time.Now(),
		CallerNumber: callerNumber,
		LastActivity: time.Now(),
		Transcript:   []models.Transcript{},
		Summary:      "",
		Suggestions:  []models.Suggestion{},
	}
	
	// Store the session
	activeCallsMutex.Lock()
	activeCalls[fallbackCallID] = session
	activeCallsMutex.Unlock()
	
	// Try to create a ticket for the fallback call
	firestoreClient, ferr := services.GetFirestoreClient()
	if ferr == nil {
		ticketID, terr := firestoreClient.CreateTicketForCall(fallbackCallID, agent_id, callerNumber)
		if terr == nil {
			session.TicketID = ticketID
			log.Printf("Created fallback ticket %s for call %s", ticketID, fallbackCallID)
		}
	}
	
	// Return a friendly message to the caller
	twimlSay := &twiml.VoiceSay{
		Message: "We're currently experiencing technical difficulties. Please try again later.",
		Voice:   "alice", // Using a specific voice can sometimes help with reliability
	}
	
	twimlPause := &twiml.VoicePause{
		Length: "1",
	}
	
	twimlResult, _ := twiml.Voice([]twiml.Element{twimlSay, twimlPause})
	c.Header("Content-Type", "text/xml")
	c.String(http.StatusOK, twimlResult)
}

// handleSuccessfulCall processes a successful call registration
func handleSuccessfulCall(c *gin.Context, callID string, agent_id string, callerNumber string) {
	firestoreClient, err := services.GetFirestoreClient()
	var ticketID string
	
	if err != nil {
		log.Printf("Error getting Firestore client: %v", err)
	} else {
		// Create a new ticket with our call ID
		ticketID, err = firestoreClient.CreateTicketForCall(callID, agent_id, callerNumber)
		if err != nil {
			log.Printf("Error creating ticket: %v", err)
		} else {
			log.Printf("Created new ticket %s for call %s", ticketID, callID)
		}
	}
   
	// Create a new call session
	session := &CallSession{
		CallID:       callID,
		AgentID:      agent_id,
		StartTime:    time.Now(),
		CallerNumber: callerNumber,
		LastActivity: time.Now(),
		Transcript:   []models.Transcript{},
		Summary:      "",
		Suggestions:  []models.Suggestion{},
		TicketID:     ticketID,
	}
   
	// Store the session
	activeCallsMutex.Lock()
	activeCalls[callID] = session
	activeCallsMutex.Unlock()

	// Set up the WebSocket stream
	twilloResponse := &twiml.VoiceStream{
		Url: "wss://api.retellai.com/audio-websocket/" + callID,
		// Additional stream parameters would go here if needed
	}

	// Use <Connect> with the stream
	twilioStart := &twiml.VoiceConnect{
		InnerElements: []twiml.Element{twilloResponse},
	}

	// Generate TwiML with all elements
	twimlResult, err := twiml.Voice([]twiml.Element{twilioStart})
	if err != nil {
		log.Printf("Error generating TwiML: %v", err)
		c.JSON(http.StatusInternalServerError, "Cannot handle call at the moment")
		return
	}

	// Log the TwiML for debugging
	log.Printf("Sending TwiML response for call %s: %s", callID, twimlResult)

	c.Header("Content-Type", "text/xml")
	c.String(http.StatusOK, twimlResult)
}

// cleanupInactiveCalls periodically checks for and removes inactive calls
func cleanupInactiveCalls() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
    
	for range ticker.C {
		threshold := time.Now().Add(-30 * time.Minute)
        
		activeCallsMutex.Lock()
		for id, session := range activeCalls {
			if session.LastActivity.Before(threshold) {
				// Call has been inactive for too long, store to ticket and remove
				storeCallTranscriptToTicket(id, true)
				delete(activeCalls, id)
				log.Printf("Removed inactive call %s due to inactivity", id)
                
				// Broadcast that call is no longer active
				update := models.TranscriptUpdate{
					Type:        "transcript_update",
					CallID:      id,
					AgentID:     session.AgentID,
					Transcript:  session.Transcript,
					StartTime:   session.StartTime,
					LastUpdated: time.Now(),
					IsActive:    false,
					TicketID:    session.TicketID,
					Summary:     session.Summary,
					Suggestions: session.Suggestions,
				}
				services.GetWebSocketHub().Broadcast(id, update)
			}
		}
		activeCallsMutex.Unlock()
	}
}

// storeCallTranscriptToTicket moves the call transcript to the ticket when a call ends
func storeCallTranscriptToTicket(callID string, timedOut bool) {
	activeCallsMutex.RLock()
	session, exists := activeCalls[callID]
	activeCallsMutex.RUnlock()
    
	if !exists {
		log.Printf("Attempted to store nonexistent call: %s", callID)
		return
	}
    
	// Get Firestore client
	firestoreClient, err := services.GetFirestoreClient()
	if err != nil {
		log.Printf("Error getting Firestore client: %v", err)
		return
	}
    
	// Update the associated ticket with the transcript, summary, and suggestions
	if session.TicketID != "" {
		err = firestoreClient.UpdateTicketWithTranscriptAndAnalysis(
			session.TicketID,
			session.Transcript,
			session.Summary, 
			session.Suggestions,
		)
		
		if err != nil {
			log.Printf("Error updating ticket with data: %v", err)
		} else {
			log.Printf("Ticket %s updated with call data", session.TicketID)
            
			// If AI determined the issue was resolved, close the ticket
			if isResolved(session.Summary) {
				err = firestoreClient.CloseTicket(session.TicketID, session.Summary)
				if err != nil {
					log.Printf("Error closing ticket: %v", err)
				} else {
					log.Printf("Ticket %s automatically closed as resolved", session.TicketID)
				}
			} else {
				log.Printf("Ticket %s remains open as the issue appears unresolved", session.TicketID)
			}
		}
	}
    
	// If the call ended normally (not timed out), remove it from active calls
	if !timedOut {
		activeCallsMutex.Lock()
		delete(activeCalls, callID)
		activeCallsMutex.Unlock()
        
		// Broadcast final update indicating call is complete
		update := models.TranscriptUpdate{
			Type:        "transcript_update",
			CallID:      callID,
			AgentID:     session.AgentID,
			Transcript:  session.Transcript,
			StartTime:   session.StartTime,
			LastUpdated: time.Now(),
			IsActive:    false,
			TicketID:    session.TicketID,
			Summary:     session.Summary,
			Suggestions: session.Suggestions,
		}
		services.GetWebSocketHub().Broadcast(callID, update)
	}
}

// isResolved checks if the summary indicates the issue was resolved
func isResolved(summary string) bool {
	if summary == "" {
		return false
	}
	
	lowerSummary := strings.ToLower(summary)
	return strings.Contains(lowerSummary, "resolved") || 
		strings.Contains(lowerSummary, "fixed") ||
		strings.Contains(lowerSummary, "solved")
}

// isValidJSON checks if a byte array contains valid JSON
func isValidJSON(data []byte) bool {
	var js json.RawMessage
	return json.Unmarshal(data, &js) == nil
}

type Transcripts struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Request struct {
	ResponseID      int           `json:"response_id"`
	Transcript      []Transcripts `json:"transcript"`
	InteractionType string        `json:"interaction_type"`
}

type Response struct {
	ResponseID      int    `json:"response_id"`
	Content         string `json:"content"`
	ContentComplete bool   `json:"content_complete"`
	EndCall         bool   `json:"end_call"`
}

func Retellwshandler(c *gin.Context) {
	callID := c.Param("call_id")
	log.Printf("WebSocket connection attempt for call: %s", callID)
   
	// Create an emergency session for all incoming calls
	activeCallsMutex.Lock()
	session, exists := activeCalls[callID]
	if !exists {
		log.Printf("Connection attempt for unknown call: %s. Creating a new session.", callID)
		
		// Create an emergency session for this call ID
		session = &CallSession{
			CallID:       callID,
			AgentID:      "unknown",
			StartTime:    time.Now(),
			CallerNumber: "unknown",
			LastActivity: time.Now(),
			Transcript:   []models.Transcript{},
			Summary:      "",
			Suggestions:  []models.Suggestion{},
		}
		
		// Store the emergency session
		activeCalls[callID] = session
	}
	activeCallsMutex.Unlock()
	
	// Try to find a ticket for this call if it doesn't have one already
	if session.TicketID == "" {
    // Wrap the entire ticket handling in a recovery function
    func() {
        defer func() {
            if r := recover(); r != nil {
                log.Printf("RECOVERED from panic during ticket lookup: %v", r)
            }
        }()

        firestoreClient, err := services.GetFirestoreClient()
        if err != nil {
            log.Printf("Error getting Firestore client: %v", err)
            return
        }

        // Try to find existing ticket first
        ticket, err := firestoreClient.GetTicketByCallID(callID)
        if err != nil {
            log.Printf("Error looking up ticket for call %s: %v", callID, err)
        } else if ticket != nil {
            log.Printf("Found existing ticket %s for call %s", ticket.TicketID, callID)
            
            // Here's the issue: We need to ensure ticket is not nil AND its fields are valid
            activeCallsMutex.Lock()
            if callSession, ok := activeCalls[callID]; ok && callSession != nil {
                // Only access ticket fields after confirming it's not nil
                if ticket.TicketID != "" {
                    callSession.TicketID = ticket.TicketID
                }
                
                // Similarly, check other fields before using them
                if ticket.AgentID != "" {
                    callSession.AgentID = ticket.AgentID
                }
                
                if ticket.CallerNumber != "" {
                    callSession.CallerNumber = ticket.CallerNumber
                }
            }
            activeCallsMutex.Unlock()
        } else {
            // No ticket found, create a new one
            log.Printf("No ticket found for call %s, creating new ticket", callID)
            newTicketID, createErr := firestoreClient.CreateTicketForCall(callID, "unknown", "unknown")
            if createErr != nil {
                log.Printf("Failed to create ticket for call %s: %v", callID, createErr)
            } else if newTicketID != "" {
                log.Printf("Created emergency ticket %s for call %s", newTicketID, callID)
                
                // Update the session with the new ticket ID
                activeCallsMutex.Lock()
                if callSession, ok := activeCalls[callID]; ok && callSession != nil {
                    callSession.TicketID = newTicketID
                }
                activeCallsMutex.Unlock()
            }
        }
    }()
}
	
	// Now handle the WebSocket connection as usual
	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			return true
		},
	}

	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Printf("Error upgrading connection: %v", err)
		return
	}
	defer func() {
		conn.Close()
		// When connection closes, store the transcript to ticket
		storeCallTranscriptToTicket(callID, false)
	}()

	// Create the initial greeting response but don't add it to the transcript history  
	response := Response{
		ResponseID:      0,
		Content:         "Hello, I'm your IT support assistant. How can I help you resolve your technical issue today?",
		ContentComplete: true,
		EndCall:         false,
	}
   
	// Send the greeting to the caller, but don't save it in the transcript
	err = conn.WriteJSON(response)
	if err != nil {
		log.Printf("Error sending initial message: %v", err)
		return
	}

	// Update the session's last activity time
	session.mutex.Lock()
	session.LastActivity = time.Now()
	session.mutex.Unlock()
   
	// Broadcast the updated timestamp (but no transcript update since we're not adding the greeting)
	broadcastTranscriptUpdate(callID)

	for {
    messageType, ms, err := conn.ReadMessage()
    if err != nil {
        log.Printf("Connection closed for call %s: %v", callID, err)
        break
    }

    if messageType == websocket.TextMessage {
        var msg Request
        err = json.Unmarshal(ms, &msg)
        if err != nil {
            log.Printf("Error unmarshaling message: %v", err)
            continue
        }

        // Update the session's last activity time and process transcript updates
        session.mutex.Lock()
        session.LastActivity = time.Now()

        // Update the transcript with the latest messages
        if len(msg.Transcript) > 0 {
            // Convert and add new messages to our transcript
            transcriptUpdated := false
           
            for _, entry := range msg.Transcript {
                // Check if this message is already in our transcript to avoid duplicates
                isDuplicate := false
                for _, existing := range session.Transcript {
                    if existing.Role == entry.Role && existing.Content == entry.Content {
                        isDuplicate = true
                        break
                    }
                }
               
                if !isDuplicate {
                    session.Transcript = append(session.Transcript, models.Transcript{
                        Role:    entry.Role,
                        Content: entry.Content,
                    })
                    transcriptUpdated = true
                }
            }

            if transcriptUpdated {
                // We should unlock the mutex before broadcasting to avoid deadlocks
                session.mutex.Unlock()
                // Broadcast after unlocking
                broadcastTranscriptUpdate(callID)
                
                // CRITICAL FIX: Don't return here, we want to process the message and continue the loop
                // Handle the websocket message
                HandleWebsocketMessages(msg, conn, callID)
                
                // Continue the loop to keep the connection open
                continue
            }
        }
        
        // Only unlock if we didn't already unlock above
        session.mutex.Unlock()
        
        // Handle the message without returning
        HandleWebsocketMessages(msg, conn, callID)
    }
}
}

// Add a function to generate summary and suggestions from the transcript
func generateSummaryAndSuggestions(transcript []models.Transcript) (string, []models.Suggestion) {
	if len(transcript) < 2 {
		return "", nil
	}

	// Get OpenAI client
	client := openai.NewClient(GetOpenAISecretKey())

	// Prepare the analysis prompt
	var promptContent string
	promptContent = "Based on the following IT support call transcript, please provide:\n" +
		"1. A brief summary of the issue in Markdown format (2-3 sentences). Use appropriate markdown formatting like headings, bold text, and bullet points to highlight key points.\n" +
		"2. Three specific suggestions for resolving the issue, each with a short title and a description\n\n" +
		"Format your response as JSON like this:\n" +
		"{\n  \"summary\": \"# Issue Summary\\n\\n**User reported** problem with...\\n\\n* Key point 1\\n* Key point 2\",\n  \"suggestions\": [\n    {\"title\": \"First suggestion title\", \"description\": \"Details here\"},\n    {\"title\": \"Second suggestion title\", \"description\": \"Details here\"},\n    {\"title\": \"Third suggestion title\", \"description\": \"Details here\"}\n  ]\n}\n\n" +
		"Here's the transcript:\n\n"

	// Include the transcript
	for _, entry := range transcript {
		promptContent += fmt.Sprintf("%s: %s\n", entry.Role, entry.Content)
	}

	// Create the AI request
	messages := []openai.ChatCompletionMessage{
		{
			Role:    "system",
			Content: "You are an expert IT support analyst that helps generate concise summaries and practical solutions. Format summaries in Markdown with appropriate headings, bullet points, and emphasis.",
		},
		{
			Role:    "user",
			Content: promptContent,
		},
	}

	req := openai.ChatCompletionRequest{
		Model:       openai.GPT3Dot5Turbo,
		Messages:    messages,
		Temperature: 0.2,
	}

	// Send the request
	resp, err := client.CreateChatCompletion(context.Background(), req)
	if err != nil {
		log.Printf("Error generating summary with AI: %v", err)
		return "", nil
	}

	if len(resp.Choices) == 0 {
		log.Println("No response from AI for summary generation")
		return "", nil
	}

	// Parse the JSON response
	type AnalysisResponse struct {
		Summary     string               `json:"summary"`
		Suggestions []models.Suggestion  `json:"suggestions"`
	}

	content := resp.Choices[0].Message.Content
	var analysis AnalysisResponse

	// Try to parse the JSON
	err = json.Unmarshal([]byte(content), &analysis)
	if err != nil {
		log.Printf("Error parsing AI response as JSON: %v", err)
		// If JSON parsing fails, just use the content as summary
		return content, nil
	}

	return analysis.Summary, analysis.Suggestions
}

func HandleWebsocketMessages(msg Request, conn *websocket.Conn, callID string) {
    client := openai.NewClient(GetOpenAISecretKey())

    if msg.InteractionType == "update_only" {
        log.Println("update interaction, do nothing.")
        return
    }

    prompt := GenerateAIRequest(msg)

    req := openai.ChatCompletionRequest{
        Model:       openai.GPT3Dot5Turbo,
        Messages:    prompt,
        Stream:      true,
        MaxTokens:   200,
        Temperature: 1.0,
    }

    stream, err := client.CreateChatCompletionStream(context.Background(), req)
    if err != nil {
        log.Printf("Error creating chat completion stream: %v", err)
        // Don't close the connection here - just return from this function
        // This allows the WebSocket to remain open for future messages
        return
    }
    defer stream.Close()

    var i int
    var fullResponse string

    // Get the session to update transcript
    activeCallsMutex.RLock()
    session, exists := activeCalls[callID]
    activeCallsMutex.RUnlock()
    if !exists {
        log.Printf("Call session not found for ID: %s", callID)
        return
    }

    for {
        response, err := stream.Recv()
        if err != nil {
            var s string
            var shouldEndCall bool
            if (errors.Is(err, io.EOF) && i == 0) || (!errors.Is(err, io.EOF)) {
                s = "[ERROR] NO RESPONSE, PLEASE RETRY"
                shouldEndCall = false  // Changed from true to false to prevent ending the call on error
                log.Printf("Error receiving from stream: %v", err)
            }

            if errors.Is(err, io.EOF) && i != 0 {
                s = "\n\n###### [END] ######"
            }

            // Add the full response to the transcript when finished
            session.mutex.Lock()
            // Only add to transcript if we actually got a response
            if fullResponse != "" {
                session.Transcript = append(session.Transcript, models.Transcript{
                    Role:    "agent",
                    Content: fullResponse,
                })

                // Generate summary and suggestions after adding the agent response
                summary, suggestions := generateSummaryAndSuggestions(session.Transcript)
                session.Summary = summary
                session.Suggestions = suggestions
            }
            session.LastActivity = time.Now()
            session.mutex.Unlock()

            // Broadcast the updated transcript with summary and suggestions
            broadcastTranscriptUpdate(callID)
            
            airesponse := Response{
                ResponseID:      msg.ResponseID,
                Content:         s,
                ContentComplete: true,
                EndCall:         shouldEndCall,
            }
          
            out, err := json.Marshal(airesponse)
            if err != nil {
                log.Printf("Error marshaling response: %v", err)
                // Don't close the connection, just return
                return
            }
            err = conn.WriteMessage(websocket.TextMessage, out)
            if err != nil {
                log.Printf("Error writing message: %v", err)
                // Don't close the connection, just return
                return
            }

            break
        }
        
        if len(response.Choices) > 0 {
            s := response.Choices[0].Delta.Content
            fullResponse += s

            airesponse := Response{
                ResponseID:      msg.ResponseID,
                Content:         s,
                ContentComplete: false,
                EndCall:         false,
            }
            
            // Only log a summary of the response to avoid flooding logs
            if i == 0 || i % 10 == 0 {
                log.Printf("Streaming response chunk #%d for call %s", i, callID)
            }

            out, err := json.Marshal(airesponse)
            if err != nil {
                log.Printf("Error marshaling response chunk: %v", err)
                // Don't close the connection, just return
                return
            }

            err = conn.WriteMessage(websocket.TextMessage, out)
            if err != nil {
                log.Printf("Error writing message chunk: %v", err)
                // Don't close the connection, just return
                return
            }
        }
        i = i + 1
    }
}

func GenerateAIRequest(msg Request) []openai.ChatCompletionMessage {
	systemprompt := openai.ChatCompletionMessage{
		Role:    "system",
		Content: "You are an IT helpdesk support agent. Your primary goal is to help users resolve basic technical issues before escalating to a ticket. Follow these guidelines:\n\n1. Be friendly, professional, and patient.\n2. First diagnose the problem by asking clarifying questions.\n3. Provide step-by-step troubleshooting instructions for common issues (network, software, hardware, account access).\n4. Use clear, non-technical language when possible.\n5. If the issue seems complex or can't be resolved during the call, explain that you'll create a support ticket to escalate it to a specialist.\n6. Before ending, summarize what was discussed and any actions taken.\n7. End calls only when the issue is resolved or properly escalated via a ticket.\n\nWhen issues are successfully resolved, clearly state that the matter has been resolved.",
	}

	var airequest []openai.ChatCompletionMessage
	airequest = append(airequest, systemprompt)

	for _, response := range msg.Transcript {
		var p_response openai.ChatCompletionMessage
		if response.Role == "agent" {
			p_response.Role = "assistant"
		} else {
			p_response.Role = "user"
		}
		p_response.Content = response.Content
		airequest = append(airequest, p_response)
	}

	return airequest
}