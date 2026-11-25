package node_commands

// NodeCommandClient defines the interface for sending node command operations
type NodeCommandClient interface {
	Send(operation NodeCommandOperation) error
	GetChannel() chan NodeCommandOperation
}
