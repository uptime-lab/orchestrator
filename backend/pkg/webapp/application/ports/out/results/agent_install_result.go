package results

// AgentInstallResult represents the result of an agent installation operation
type AgentInstallResult struct {
	Message string `json:"message"`
}

// NewAgentInstallResult creates a new AgentInstallResult
func NewAgentInstallResult(message string) *AgentInstallResult {
	return &AgentInstallResult{
		Message: message,
	}
}
