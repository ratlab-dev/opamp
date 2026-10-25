package opamp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// UIEvent is broadcast to real-time UI dashboards
type UIEvent struct {
	Type      string      `json:"type"` // e.g. "agent_update", "config_update", "override_update"
	Timestamp string      `json:"timestamp"`
	Data      interface{} `json:"data,omitempty"`
}

// EventBroker distributes real-time events to connected HTTP SSE clients
type EventBroker struct {
	mu      sync.Mutex
	clients map[chan []byte]bool
}

func NewEventBroker() *EventBroker {
	return &EventBroker{
		clients: make(map[chan []byte]bool),
	}
}

// Broadcast sends a real-time event to all active SSE browser connections
func (b *EventBroker) Broadcast(eventType string, data interface{}) {
	evt := UIEvent{
		Type:      eventType,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Data:      data,
	}

	payload, err := json.Marshal(evt)
	if err != nil {
		return
	}

	sseMsg := fmt.Sprintf("event: %s\ndata: %s\n\n", eventType, string(payload))
	msgBytes := []byte(sseMsg)

	b.mu.Lock()
	defer b.mu.Unlock()

	for ch := range b.clients {
		select {
		case ch <- msgBytes:
		default:
			// Client channel full or slow, skip to prevent stalling
		}
	}
}

// ServeSSE handles the GET /api/events SSE HTTP connection
func (b *EventBroker) ServeSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	clientChan := make(chan []byte, 32)

	b.mu.Lock()
	b.clients[clientChan] = true
	b.mu.Unlock()

	defer func() {
		b.mu.Lock()
		delete(b.clients, clientChan)
		close(clientChan)
		b.mu.Unlock()
	}()

	// Send initial ping
	_, _ = w.Write([]byte("event: connected\ndata: {\"status\":\"connected\"}\n\n"))
	flusher.Flush()

	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			// Keepalive comment
			_, err := w.Write([]byte(": keepalive\n\n"))
			if err != nil {
				return
			}
			flusher.Flush()
		case msg, ok := <-clientChan:
			if !ok {
				return
			}
			_, err := w.Write(msg)
			if err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
