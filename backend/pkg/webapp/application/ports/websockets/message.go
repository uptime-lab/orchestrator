package websockets

import (
	"encoding/json"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"

	"github.com/google/uuid"
)

const InboundMessageTypeAuthorization = "authorization"

type MessagePayload interface {
	ToBytes() []byte
}

type MessageWrapper struct {
	Type    string      `json:"type"`
	Payload interface{} `json:"payload"`
}

func (m MessageWrapper) ToBytes() []byte {
	result, _ := json.Marshal(m)
	return result
}

type MessageAuthStatusPayload struct {
	Status string  `json:"status"`
	Error  *string `json:"error"`
}

type MessageAuthStatus struct {
	Type    string                   `json:"type"`
	Payload MessageAuthStatusPayload `json:"payload"`
}

func (m MessageAuthStatus) ToBytes() []byte {
	result, _ := json.Marshal(m)
	return result
}

func NewMessageAuthorizationStatusOk() MessageAuthStatus {
	return newMessageAuthorizationStatus("ok", nil)
}

func NewMessageAuthorizationStatusError(error string) MessageAuthStatus {
	return newMessageAuthorizationStatus("failed", &error)
}

func newMessageAuthorizationStatus(status string, error *string) MessageAuthStatus {
	return MessageAuthStatus{
		Type:    "authorization_status",
		Payload: MessageAuthStatusPayload{Status: status, Error: error},
	}
}

type Message struct {
	UserID    *uuid.UUID     `json:"user_id"`
	AccountID *uuid.UUID     `json:"account_id"`
	Payload   MessagePayload `json:"payload"`

	// ScopeOU restricts an account-wide message to recipients who may read that
	// organizational unit. Nil (with ScopeOUUnassigned false) means "no OU
	// scope": the message describes an account-global resource (images, an
	// adoption refusal) and is filtered by RequiredAction instead — it is NOT
	// "every member of the account", which is what it once meant and what let
	// a member holding no binding receive frames their GET would have refused.
	//
	// Routing metadata only — never serialized to clients.
	ScopeOU *uuid.UUID `json:"-"`
	// ScopeOUUnassigned marks a message about a resource that belongs to NO OU.
	// Those are visible only to account-wide holders, mirroring how the list
	// handlers treat OU-less resources.
	ScopeOUUnassigned bool `json:"-"`
	// RequiredAction is the permission of the account-global message's HTTP
	// representation: the action the REST route that lists the same resource is
	// gated on (`GET /images` → image.read, `GET /nodes/adoption-refusals` →
	// node.create). The hub asks the authz enforcer the question that route's
	// RequireAction middleware asks, so a live update is visible exactly to
	// those who could GET it. Empty is fail-closed — the frame reaches nobody —
	// because "no action declared" is the state that used to mean "everyone".
	//
	// Routing metadata only — never serialized to clients. On an OU-scoped
	// message it is optional and ADDITIONAL to the node.read check through
	// ScopeOU (see RequiringAction): a resource that lives in a unit but whose
	// REST route also demands an action of its own needs both.
	RequiredAction authz.Action `json:"-"`

	// FullPayloadAction splits the frame by recipient the way a REST read
	// redacts by caller: a recipient holding this action account-wide (nil
	// target, the question the handler asks) receives Payload; every other
	// admitted recipient receives RedactedPayload, or nothing when that is
	// nil. Empty means every admitted recipient gets Payload. Set it through
	// WithRedactedVariant.
	//
	// Routing metadata only — never serialized to clients.
	FullPayloadAction authz.Action   `json:"-"`
	RedactedPayload   MessagePayload `json:"-"`
}

// RequiringAction adds the action a resource's REST route demands on top of the
// OU scope of an OU-scoped message: a recipient must also hold it account-wide
// (nil target), as middleware.RequireAction checks on that route. Without it a
// frame about a unit's run reached every node.read holder of the unit although
// the run's REST read is refused to them.
func (m Message) RequiringAction(action authz.Action) Message {
	m.RequiredAction = action
	return m
}

// WithRedactedVariant makes the frame carry Payload only to recipients holding
// fullAction account-wide and `redacted` to everyone else the message reaches,
// mirroring a REST read that redacts for callers without that action. The
// push channel is a second read path to the same object, so it may not carry
// more than the first one serves to the same member. A nil `redacted` sends
// the others nothing at all: the frame for an object their REST read omits
// entirely.
func (m Message) WithRedactedVariant(fullAction authz.Action, redacted MessagePayload) Message {
	m.FullPayloadAction = fullAction
	m.RedactedPayload = redacted
	return m
}

// ToJSON serializes the Message struct to JSON
func (m Message) ToJSON() ([]byte, error) {
	type MessageTemp struct {
		UserID    *uuid.UUID      `json:"user_id"`
		AccountID *uuid.UUID      `json:"account_id"`
		Payload   json.RawMessage `json:"payload"`
	}

	// Create a temporary struct with the payload converted to RawMessage
	temp := MessageTemp{
		UserID:    m.UserID,
		AccountID: m.AccountID,
		Payload:   m.Payload.ToBytes(),
	}

	// Marshal the temporary struct to JSON
	return json.Marshal(temp)
}

// FromJSON deserializes JSON into a Message struct
func FromJSON(data []byte) (Message, error) {
	// Create a temporary structure to hold the parsed JSON
	type MessageTemp struct {
		UserID    *uuid.UUID      `json:"user_id"`
		AccountID *uuid.UUID      `json:"account_id"`
		Payload   json.RawMessage `json:"payload"`
	}

	var temp MessageTemp
	if err := json.Unmarshal(data, &temp); err != nil {
		return Message{}, err
	}

	// Create a RawJSONPayload from the raw payload data
	payload := RawJSONPayload(temp.Payload)

	// Create the final message
	return Message{
		UserID:    temp.UserID,
		AccountID: temp.AccountID,
		Payload:   payload,
	}, nil
}

// RawJSONPayload implements MessagePayload for raw JSON data
type RawJSONPayload []byte

// ToBytes returns the raw JSON data
func (r RawJSONPayload) ToBytes() []byte {
	return r
}

// FromMap creates a Message from a map[string]interface{}
func FromMap(data map[string]interface{}) (Message, error) {
	// First, encode the map to JSON
	jsonData, err := json.Marshal(data)
	if err != nil {
		return Message{}, err
	}

	// Then decode the JSON back to a Message
	return FromJSON(jsonData)
}

type InboundMessage struct {
	Type     string          `json:"type"`
	Payload  json.RawMessage `json:"payload"`
	ClientID ClientID        `json:"-"`
}

type AuthorizationPayload struct {
	Token string `json:"token"`
}

func NewUserMessage(userID uuid.UUID, payload MessagePayload) Message {
	return Message{
		UserID:  &userID,
		Payload: payload,
	}
}

// NewAccountMessage builds a frame about an account-global resource, delivered
// to every socket of the account whose principal holds `action` account-wide —
// the same check `middleware.RequireAction(enforcer, action)` performs on the
// REST route that lists the resource. Callers MUST pass the action of that
// route, never a looser one: the push channel is a second read path to the same
// data, and the permission it enforces has to be the one the first path
// enforces or the two drift (the channel once enforced none).
// The action is a required parameter rather than a field precisely so a new
// sender cannot forget to declare one; an empty action compiles but delivers
// nothing, and a source-scan test refuses a literal empty argument.
//
// For a resource that lives in an organizational unit use
// NewAccountMessageForOU instead.
func NewAccountMessage(accountID uuid.UUID, action authz.Action, payload MessagePayload) Message {
	return Message{
		AccountID:      &accountID,
		Payload:        payload,
		RequiredAction: action,
	}
}

// NewAccountMessageForOU is NewAccountMessage for a resource that lives in an
// organizational unit. Recipients who cannot read that OU over HTTP do not
// receive the frame either — without this the push channel handed an OU-scoped
// viewer the node state, hostnames, IPs and run progress of OUs their lists
// carefully hide. A nil ouID means the resource is unassigned, which only
// account-wide holders may see.
func NewAccountMessageForOU(accountID uuid.UUID, ouID *uuid.UUID, payload MessagePayload) Message {
	return Message{
		AccountID:         &accountID,
		Payload:           payload,
		ScopeOU:           ouID,
		ScopeOUUnassigned: ouID == nil,
	}
}

func NewSystemMessage(payload MessagePayload) Message {
	return Message{
		Payload: payload,
	}
}

// MessageErrorPayload represents the payload for an error message
type MessageErrorPayload struct {
	Message string `json:"message"`
}

// MessageError represents an error message
type MessageError struct {
	Type    string              `json:"type"`
	Payload MessageErrorPayload `json:"payload"`
}

func (m MessageError) ToBytes() []byte {
	result, _ := json.Marshal(m)
	return result
}

// NewMessageError creates a new error message with the given message
func NewMessageError(message string) MessageError {
	return MessageError{
		Type:    "error",
		Payload: MessageErrorPayload{Message: message},
	}
}
