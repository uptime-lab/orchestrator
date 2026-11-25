package services

// TerminalExit reports why an interactive terminal session ended.
type TerminalExit struct {
	Code  int32
	Error string
}

// TerminalSession is a live browser↔device terminal bridge. Output() streams
// bytes coming from the device PTY; Closed() fires once when the session ends.
type TerminalSession interface {
	SessionID() string
	Output() <-chan []byte
	Closed() <-chan TerminalExit
}

// TerminalService bridges a browser WebSocket to a device PTY over the agent's
// existing gRPC stream. Implemented by the gRPC server.
type TerminalService interface {
	// OpenTerminal starts a session on a connected node and returns it. Errors
	// if the node is not currently connected.
	OpenTerminal(nodeID string, cols, rows uint32) (TerminalSession, error)
	// SendTerminalInput forwards keystrokes (or a resize) to the device.
	SendTerminalInput(sessionID string, data []byte, isResize bool, cols, rows uint32)
	// CloseTerminal tears down a session and tells the device to end its PTY.
	CloseTerminal(sessionID string)
}
