package websockets

import (
	websockets2 "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/websockets"
)

// WebsocketClient interface defines how to send messages to a websocket client
type WebsocketClient interface {
	Send(websockets2.Message) error
	GetChannel() chan websockets2.Message
}

// WebsocketClientChannel implements WebsocketClient using a direct channel
type WebsocketClientChannel struct {
	channel chan websockets2.Message
}

// Send sends a message to the client through the channel
func (c *WebsocketClientChannel) Send(msg websockets2.Message) error {
	c.channel <- msg
	return nil
}

// GetChannel returns the underlying channel
func (c *WebsocketClientChannel) GetChannel() chan websockets2.Message {
	return c.channel
}

// NewWebsocketClientChannel creates a new channel-based WebsocketClient
func NewWebsocketClientChannel() *WebsocketClientChannel {
	client := &WebsocketClientChannel{
		channel: make(chan websockets2.Message, 10000),
	}

	return client
}
