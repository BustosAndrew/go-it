package main

import (
	"bytes"
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
	TicketID      string  // Add field to store the associated ticket ID
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
    
	// CORS middleware
	app.Use(func(c *gin.Context) {
		c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
		c.Writer.Header().Set("Access-Control-Allow-Credentials", "true")
		c.Writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization, accept, origin, Cache-Control, X-Requested-With")
		c.Writer.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS, GET, PUT, DELETE")

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}

		c.Next()
	})
    
	// API endpoints
	app.Any("/llm-websocket/:call_id", Retellwshandler)
	app.POST("/twilio-webhook/:agent_id", Twiliowebhookhandler)
    
	// New endpoint for frontend WebSocket connections
	app.GET("/frontend-ws/:call_id", FrontendWebSocketHandler)
    
	// Endpoint to list active calls
	app.GET("/api/calls/active", GetActiveCallsHandler)
    
	// New endpoint to retrieve all past call transcripts
	app.GET("/api/calls/history", GetCallHistoryHandler)
    
	// Add new endpoint for tickets
	app.GET("/api/tickets", GetAllTicketsHandler)
	app.GET("/api/tickets/:ticket_id", GetTicketByIDHandler)
	app.POST("/api/tickets/:ticket_id/close", CloseTicketHandler) // New endpoint to manually close tickets
    
	app.Run(":" + port)
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
		}
		activeCallsList = append(activeCallsList, callData)
	}
    
	c.JSON(http.StatusOK, gin.H{
		"status": "success",
		"calls":  activeCallsList,
	})
}

// GetCallHistoryHandler returns a list of all past call transcripts
func GetCallHistoryHandler(c *gin.Context) {
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
    
	// Retrieve all call transcripts
	transcripts, err := firestoreClient.GetAllCallTranscripts()
	if err != nil {
		log.Printf("Error retrieving call transcripts: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"status": "error",
			"error":  "Failed to retrieve call history",
		})
		return
	}
    
	// Transform transcripts to a suitable response format
	callHistory := make([]map[string]interface{}, 0, len(transcripts))
	for _, transcript := range transcripts {
		callData := map[string]interface{}{
			"call_id":       transcript.CallID,
			"agent_id":      transcript.AgentID,
			"start_time":    transcript.StartTime,
			"end_time":      transcript.EndTime,
			"caller_number": transcript.CallerNumber,
			"duration_secs": transcript.DurationSecs,
			"transcript":    transcript.Transcript,
		}
		callHistory = append(callHistory, callData)
	}
    
	// Return the response
	c.JSON(http.StatusOK, gin.H{
		"status": "success",
		"calls":  callHistory,
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
		}
		session.mutex.Unlock()
        
		client.Hub.Broadcast(callID, update)
	} else {
		// Try to get from Firestore if call is completed
		firestoreClient, err := services.GetFirestoreClient()
		if err != nil {
			log.Printf("Error getting Firestore client: %v", err)
		} else {
			transcript, err := firestoreClient.GetCallTranscript(callID)
			if err != nil {
				log.Printf("Error retrieving call transcript: %v", err)
			} else if transcript != nil {
				// Send the archived transcript
				update := models.TranscriptUpdate{
					Type:        "transcript_update",
					CallID:      transcript.CallID,
					AgentID:     transcript.AgentID,
					Transcript:  transcript.Transcript,
					StartTime:   transcript.StartTime,
					LastUpdated: transcript.EndTime,
					IsActive:    false,
				}
                
				client.Hub.Broadcast(callID, update)
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

func Twiliowebhookhandler(c *gin.Context) {
	agent_id := c.Param("agent_id")
	callerNumber := c.PostForm("From")

	callinfo, err := RegisterRetellCall(agent_id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, "cannot handle call atm")
		return
	}
   
	// Create a ticket for the new call
	firestoreClient, err := services.GetFirestoreClient()
	var ticketID string
	if err != nil {
		log.Printf("Error getting Firestore client: %v", err)
	} else {
		// Create a ticket for this call
		ticketID, err = firestoreClient.CreateTicketForCall(callinfo.CallID, agent_id, callerNumber)
		if err != nil {
			log.Printf("Error creating ticket: %v", err)
		}
	}
   
	// Create a new call session
	session := &CallSession{
		CallID:       callinfo.CallID,
		AgentID:      agent_id,
		StartTime:    time.Now(),
		CallerNumber: callerNumber,
		LastActivity: time.Now(),
		Transcript:   []models.Transcript{},
		TicketID:     ticketID, // Store the ticket ID with the session
	}
   
	// Store the session
	activeCallsMutex.Lock()
	activeCalls[callinfo.CallID] = session
	activeCallsMutex.Unlock()

	twilloresponse := &twiml.VoiceStream{
		Url: "wss://api.retellai.com/audio-websocket/" + callinfo.CallID,
	}

	twiliostart := &twiml.VoiceConnect{
		InnerElements: []twiml.Element{twilloresponse},
	}

	twimlResult, err := twiml.Voice([]twiml.Element{twiliostart})
	if err != nil {
		c.JSON(http.StatusInternalServerError, "cannot handle call atm")
		return
	}

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
				// Call has been inactive for too long, store transcript and remove
				storeCallTranscript(id, true)
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
				}
				services.GetWebSocketHub().Broadcast(id, update)
			}
		}
		activeCallsMutex.Unlock()
	}
}

// storeCallTranscript stores the call transcript in Firestore and updates the associated ticket
func storeCallTranscript(callID string, timedOut bool) {
	activeCallsMutex.RLock()
	session, exists := activeCalls[callID]
	activeCallsMutex.RUnlock()
    
	if !exists {
		log.Printf("Attempted to store nonexistent call: %s", callID)
		return
	}
    
	// Create call transcript record
	endTime := time.Now()
	callData := models.CallTranscript{
		CallID:       callID,
		AgentID:      session.AgentID,
		Transcript:   session.Transcript,
		StartTime:    session.StartTime,
		EndTime:      endTime,
		DurationSecs: int(endTime.Sub(session.StartTime).Seconds()),
		CallerNumber: session.CallerNumber,
	}
    
	// Store in Firestore
	firestoreClient, err := services.GetFirestoreClient()
	if err != nil {
		log.Printf("Error getting Firestore client: %v", err)
		return
	}
    
	// Save call transcript
	docID, err := firestoreClient.SaveCallTranscript(callData)
	if err != nil {
		log.Printf("Error saving call transcript to Firestore: %v", err)
		return
	}
    
	log.Printf("Call transcript stored with ID: %s", docID)
    
	// Update the associated ticket with the transcript
	if session.TicketID != "" {
		// First, just update the transcript without changing status
		err = firestoreClient.UpdateTicketWithTranscript(session.TicketID, session.Transcript)
		if err != nil {
			log.Printf("Error updating ticket with transcript: %v", err)
		} else {
			log.Printf("Ticket %s updated with call transcript", session.TicketID)
            
			// Analyze if the issue was resolved using AI
			resolved, resolution := analyzeTranscriptForResolution(session.Transcript)
            
			if resolved {
				// If AI determined the issue was resolved, close the ticket
				err = firestoreClient.CloseTicket(session.TicketID, resolution)
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
			LastUpdated: endTime,
			IsActive:    false,
		}
		services.GetWebSocketHub().Broadcast(callID, update)
	}
}

// analyzeTranscriptForResolution uses AI to determine if the issue was resolved
func analyzeTranscriptForResolution(transcript []models.Transcript) (bool, string) {
	if len(transcript) < 3 {
		// Not enough interaction to determine resolution
		return false, ""
	}
    
	// Get OpenAI client
	client := openai.NewClient(GetOpenAISecretKey())
    
	// Prepare the analysis prompt
	var promptContent string
	promptContent = "This is a transcript of a customer service call. Please analyze if the issue was fully resolved. Answer with 'RESOLVED' or 'UNRESOLVED' followed by a brief summary of the call and resolution status.\n\n"
    
	// Include the transcript
	for _, entry := range transcript {
		promptContent += fmt.Sprintf("%s: %s\n", entry.Role, entry.Content)
	}
    
	// Create the AI request
	messages := []openai.ChatCompletionMessage{
		{
			Role:    "system",
			Content: "You are an expert call analyzer. Your job is to determine if a customer issue has been resolved based on call transcripts.",
		},
		{
			Role:    "user",
			Content: promptContent,
		},
	}
    
	req := openai.ChatCompletionRequest{
		Model:       openai.GPT3Dot5Turbo,
		Messages:    messages,
		MaxTokens:   300,
		Temperature: 0.2, // Lower temperature for more consistent and predictable results
	}
    
	// Send the request
	resp, err := client.CreateChatCompletion(context.Background(), req)
	if err != nil {
		log.Printf("Error analyzing transcript with AI: %v", err)
		return false, ""
	}
    
	if len(resp.Choices) == 0 {
		log.Println("No response from AI analyzer")
		return false, ""
	}
    
	// Get the analysis result
	analysis := resp.Choices[0].Message.Content
    
	// Determine if resolved based on the AI response
	resolved := strings.HasPrefix(strings.ToUpper(analysis), "RESOLVED")
    
	// Extract the summary, removing the RESOLVED/UNRESOLVED prefix
	summary := analysis
	if idx := strings.Index(strings.ToUpper(analysis), "RESOLVED"); idx != -1 {
		summary = strings.TrimSpace(analysis[idx+8:])
	} else if idx := strings.Index(strings.ToUpper(analysis), "UNRESOLVED"); idx != -1 {
		summary = strings.TrimSpace(analysis[idx+10:])
	}
    
	return resolved, summary
}

func RegisterRetellCall(agent_id string) (RegisterCallResponse, error) {
	request := RegisterCallRequest{
		AgentID:                agent_id,
		AudioEncoding:          "mulaw",
		SampleRate:             8000,
		AudioWebsocketProtocol: "twilio",
	}

	request_bytes, err := json.Marshal(request)
	if err != nil {
		return RegisterCallResponse{}, err
	}

	payload := bytes.NewBuffer(request_bytes)

	request_url := "https://api.retellai.com/register-call"
	method := "POST"

	var bearer = "Bearer " + GetRetellAISecretKey()

	client := &http.Client{}
	req, err := http.NewRequest(method, request_url, payload)
	if err != nil {
		return RegisterCallResponse{}, err
	}

	req.Header.Add("Authorization", bearer)
	req.Header.Add("Content-Type", "application/json")
	res, err := client.Do(req)
	if err != nil {
		return RegisterCallResponse{}, err
	}
	defer res.Body.Close()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		return RegisterCallResponse{}, err
	}

	var response RegisterCallResponse

	json.Unmarshal(body, &response)

	return response, nil
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
   
	// Verify the call is registered
	activeCallsMutex.RLock()
	session, exists := activeCalls[callID]
	activeCallsMutex.RUnlock()
   
	if !exists {
		log.Printf("Connection attempt for unknown call: %s", callID)
		c.JSON(http.StatusNotFound, gin.H{"error": "Call not found"})
		return
	}
   
	upgrader := websocket.Upgrader{}

	upgrader.CheckOrigin = func(r *http.Request) bool {
		return true
	}

	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Printf("Error upgrading connection: %v", err)
		return
	}
   
	defer func() {
		conn.Close()
		// When connection closes, store the transcript
		storeCallTranscript(callID, false)
	}()

	response := Response{
		ResponseID:      0,
		Content:         "Hello, I am Susie, Andrew Tate's most trusted mistress. How can I help you get out of the matrix?",
		ContentComplete: true,
		EndCall:         false,
	}
   
	// Update the transcript with initial agent message
	session.mutex.Lock()
	session.Transcript = append(session.Transcript, models.Transcript{
		Role:    "agent",
		Content: response.Content,
	})
	session.LastActivity = time.Now()
	session.mutex.Unlock()
   
	// Broadcast the updated transcript to frontend clients
	broadcastTranscriptUpdate(callID)

	err = conn.WriteJSON(response)
	if err != nil {
		log.Printf("Error sending initial message: %v", err)
		return
	}

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
           
			// Update the session's last activity time
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
					// Use mutex unlock and lock to prevent deadlocks
					session.mutex.Unlock()
					// Broadcast updated transcript to frontend
					broadcastTranscriptUpdate(callID)
					session.mutex.Lock()
				}
			}
			session.mutex.Unlock()
          
			HandleWebsocketMessages(msg, conn, callID)
		}
	}
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
		log.Println(err)
		conn.Close()
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
				shouldEndCall = true
			}

			if errors.Is(err, io.EOF) && i != 0 {
				s = "\n\n###### [END] ######"
				
				// Add the full response to the transcript when finished
				session.mutex.Lock()
				session.Transcript = append(session.Transcript, models.Transcript{
					Role:    "agent",
					Content: fullResponse,
				})
				session.LastActivity = time.Now()
				session.mutex.Unlock()
				
				// Broadcast the updated transcript
				broadcastTranscriptUpdate(callID)
			}
			
			airesponse := Response{
				ResponseID:      msg.ResponseID,
				Content:         s,
				ContentComplete: true,
				EndCall:         shouldEndCall,
			}
		  
			out, err := json.Marshal(airesponse)
			if err != nil {
				log.Println(err)
				conn.Close()
				return
			}

			err = conn.WriteMessage(websocket.TextMessage, out)
			if err != nil {
				log.Println(err)
				conn.Close()
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
			log.Println(airesponse)

			out, err := json.Marshal(airesponse)
			if err != nil {
				log.Println(err)
				conn.Close()
				return
			}

			err = conn.WriteMessage(websocket.TextMessage, out)
			if err != nil {
				log.Println(err)
				conn.Close()
				return
			}
		}
		i = i + 1
	}
}

func GenerateAIRequest(msg Request) []openai.ChatCompletionMessage {
	systemprompt := openai.ChatCompletionMessage{
		Role:    "system",
		Content: "You are an AI phone assistant designed to embody the persona of an Andrew Tate-style motivator, delivering brutally honest, no-nonsense advice and roasts during conversations. Your tone is unapologetically confident, direct, and peppered with occasional cussing to emphasize points. You should aim to inspire action by challenging the user’s excuses and behaviors, often referencing concepts like ‘the Matrix,’ discipline, and personal accountability. If interrupted, seamlessly adjust your response to acknowledge the interruption while maintaining the same tone and persona. Use concise, impactful language, and adapt your replies to keep the energy of the conversation intense and engaging. Stay aligned with the meme-like nature of Andrew Tate-style conversations, keeping interactions entertaining, provocative, and sometimes borderline absurd—but never crossing the line into harmful or discriminatory behavior.",
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