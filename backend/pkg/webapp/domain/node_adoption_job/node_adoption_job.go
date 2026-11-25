// Package node_adoption_job models an asynchronous "adopt a device over SSH"
// run: connect, install the agent, register the node, wait for it to come
// online and (optionally) flash a network-first EEPROM. Progress is a fixed,
// ordered list of steps so the UI can render a live checklist.
package node_adoption_job

import (
	"time"

	"github.com/google/uuid"
)

// JobStatus is the overall run status.
type JobStatus string

const (
	StatusPending   JobStatus = "pending"
	StatusRunning   JobStatus = "running"
	StatusCompleted JobStatus = "completed"
	StatusFailed    JobStatus = "failed"
)

// StepStatus is the status of one ordered step.
type StepStatus string

const (
	StepPending   StepStatus = "pending"
	StepRunning   StepStatus = "running"
	StepCompleted StepStatus = "completed"
	StepFailed    StepStatus = "failed"
	StepSkipped   StepStatus = "skipped"
)

// Step keys (stable identifiers used by the UI + engine).
const (
	StepConnect       = "connect"
	StepProbe         = "probe"
	StepCreateNode    = "create_node"
	StepInstallAgent  = "install_agent"
	StepWriteAdoption = "write_adoption"
	StepStartService  = "start_service"
	StepWaitAgent     = "wait_agent"
	StepEEPROM        = "eeprom"
)

// Step is one ordered stage of the run.
type Step struct {
	Key     string     `json:"key"`
	Label   string     `json:"label"`
	Status  StepStatus `json:"status"`
	Message string     `json:"message,omitempty"`
}

// Job is one SSH adoption run.
type Job struct {
	ID                 uuid.UUID
	AccountID          uuid.UUID
	NodeID             *uuid.UUID // set once the node record is created
	Host               string
	Username           string
	Status             JobStatus
	Steps              []Step
	HostKeyFingerprint string
	Error              string
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// DefaultSteps returns the ordered step list in the pending state.
func DefaultSteps() []Step {
	return []Step{
		{Key: StepConnect, Label: "Connect over SSH", Status: StepPending},
		{Key: StepProbe, Label: "Probe device", Status: StepPending},
		{Key: StepInstallAgent, Label: "Install agent", Status: StepPending},
		{Key: StepCreateNode, Label: "Create node record", Status: StepPending},
		{Key: StepWriteAdoption, Label: "Write adoption file", Status: StepPending},
		{Key: StepStartService, Label: "Start agent service", Status: StepPending},
		{Key: StepWaitAgent, Label: "Wait for agent to connect", Status: StepPending},
		{Key: StepEEPROM, Label: "Flash network-first EEPROM", Status: StepPending},
	}
}

// NewJob creates a pending job with the default step list.
func NewJob(accountID uuid.UUID, host, username string) *Job {
	now := time.Now()
	return &Job{
		ID:        uuid.New(),
		AccountID: accountID,
		Host:      host,
		Username:  username,
		Status:    StatusPending,
		Steps:     DefaultSteps(),
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// SetStep updates a step's status/message (no-op if the key is unknown).
func (j *Job) SetStep(key string, status StepStatus, message string) {
	for i := range j.Steps {
		if j.Steps[i].Key == key {
			j.Steps[i].Status = status
			j.Steps[i].Message = message
			break
		}
	}
	j.UpdatedAt = time.Now()
}

// SkipStep marks a step skipped with a reason.
func (j *Job) SkipStep(key, reason string) { j.SetStep(key, StepSkipped, reason) }

// Fail marks the run failed with an error message.
func (j *Job) Fail(msg string) {
	j.Status = StatusFailed
	j.Error = msg
	j.UpdatedAt = time.Now()
}

// Complete marks the run completed.
func (j *Job) Complete() {
	j.Status = StatusCompleted
	j.UpdatedAt = time.Now()
}
