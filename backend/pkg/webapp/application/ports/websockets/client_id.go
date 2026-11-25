package websockets

import (
	"crypto/rand"
	"encoding/hex"
)

// ClientID represents a unique identifier for a WebSocket client
type ClientID string

// NewClientID generates a new unique client ID
func NewClientID() ClientID {
	bytes := make([]byte, 16)
	_, err := rand.Read(bytes)
	if err != nil {
		// Fallback to a simple counter-based ID if crypto/rand fails
		return ClientID(hex.EncodeToString([]byte("fallback")))
	}
	return ClientID(hex.EncodeToString(bytes))
}

// String returns the string representation of the ClientID
func (c ClientID) String() string {
	return string(c)
}
