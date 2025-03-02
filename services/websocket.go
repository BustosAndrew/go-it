package services

import (
	"encoding/json"
	"log"
	"sync"

	"github.com/gorilla/websocket"
)

// Client represents a connected WebSocket client
type Client struct {
	ID     string
	Conn   *websocket.Conn
	CallID string
	Send   chan []byte
	Hub    *WebSocketHub
}

// WebSocketHub maintains the set of active clients and broadcasts messages to the clients
type WebSocketHub struct {
	// Registered clients
	clients map[*Client]bool

	// Clients organized by call ID
	callClients map[string][]*Client

	// Register requests from the clients - Changed to uppercase for export
	Register chan *Client

	// Unregister requests from clients - Changed to uppercase for export
	Unregister chan *Client

	// Mutex for safe concurrent access
	mutex sync.RWMutex
}

// NewWebSocketHub creates a new WebSocketHub instance
func NewWebSocketHub() *WebSocketHub {
	return &WebSocketHub{
		Register:    make(chan *Client),
		Unregister:  make(chan *Client),
		clients:     make(map[*Client]bool),
		callClients: make(map[string][]*Client),
	}
}

// Run starts the WebSocketHub
func (h *WebSocketHub) Run() {
	for {
		select {
		case client := <-h.Register:
			h.mutex.Lock()
			h.clients[client] = true
			if client.CallID != "" {
				h.callClients[client.CallID] = append(h.callClients[client.CallID], client)
			}
			h.mutex.Unlock()
		case client := <-h.Unregister:
			h.mutex.Lock()
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client.Send)

				// Remove from call clients
				if client.CallID != "" {
					clients := h.callClients[client.CallID]
					for i, c := range clients {
						if c == client {
							h.callClients[client.CallID] = append(clients[:i], clients[i+1:]...)
							break
						}
					}

					// If no more clients for this call ID, remove the entry
					if len(h.callClients[client.CallID]) == 0 {
						delete(h.callClients, client.CallID)
					}
				}
			}
			h.mutex.Unlock()
		}
	}
}

// Broadcast sends a message to all clients subscribed to a specific call
func (h *WebSocketHub) Broadcast(callID string, message interface{}) {
	data, err := json.Marshal(message)
	if err != nil {
		log.Printf("Error marshaling broadcast message: %v", err)
		return
	}

	h.mutex.RLock()
	defer h.mutex.RUnlock()

	clients, ok := h.callClients[callID]
	if !ok {
		// No clients are listening for this call
		return
	}

	for _, client := range clients {
		select {
		case client.Send <- data:
		default:
			log.Printf("Failed to send message to client, buffer full")
			close(client.Send)
			delete(h.clients, client)
		}
	}
}

// WritePump pumps messages from the hub to the websocket connection
func (c *Client) WritePump() {
	defer func() {
		c.Conn.Close()
	}()

	for {
		message, ok := <-c.Send
		if !ok {
			// The hub closed the channel
			c.Conn.WriteMessage(websocket.CloseMessage, []byte{})
			return
		}

		err := c.Conn.WriteMessage(websocket.TextMessage, message)
		if err != nil {
			log.Printf("Error writing to WebSocket: %v", err)
			return
		}
	}
}

// Global instance of the WebSocket hub
var wsHub *WebSocketHub
var wsHubOnce sync.Once

// GetWebSocketHub returns the singleton WebSocket hub
func GetWebSocketHub() *WebSocketHub {
	wsHubOnce.Do(func() {
		wsHub = NewWebSocketHub()
		go wsHub.Run()
	})
	return wsHub
}
