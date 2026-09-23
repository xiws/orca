package domain

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/xiws/orca/internal/model"
)

type RunID int64
type InvocationID int64
type InputRequestID int64

func NewID() int64 {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return int64(binary.BigEndian.Uint64(b[:])&((1<<63)-1)) | 1
}

type RunState string

const (
	Queued      RunState = "queued"
	Running     RunState = "running"
	Waiting     RunState = "waiting"
	Interrupted RunState = "interrupted"
	Reconciling RunState = "reconciling"
	Cancelling  RunState = "cancelling"
	Succeeded   RunState = "succeeded"
	Failed      RunState = "failed"
	Cancelled   RunState = "cancelled"
)

func (s RunState) Terminal() bool { return s == Succeeded || s == Failed || s == Cancelled }

func CanTransition(from, to RunState) bool {
	if from == to {
		return true
	}
	if from.Terminal() {
		return false
	}
	if to == Cancelling {
		return true
	}
	switch from {
	case Queued:
		return to == Running || to == Interrupted || to == Failed
	case Running:
		return to == Waiting || to == Interrupted || to == Reconciling || to == Succeeded || to == Failed
	case Waiting:
		return to == Running || to == Interrupted || to == Reconciling || to == Failed
	case Interrupted:
		return to == Running || to == Reconciling || to == Failed
	case Reconciling:
		return to == Interrupted || to == Failed
	case Cancelling:
		return to == Cancelled || to == Reconciling
	}
	return false
}

type Policy struct {
	Tools       []string `json:"tools"`
	AutoApprove []string `json:"auto_approve,omitempty"`
}

func (p Policy) Allows(name string) bool {
	for _, t := range p.Tools {
		if t == name {
			return true
		}
	}
	return false
}
func (p Policy) Approved(name string) bool {
	for _, t := range p.AutoApprove {
		if t == name {
			return true
		}
	}
	return false
}
func (p Policy) Intersect(other Policy) Policy {
	r := Policy{Tools: []string{}}
	for _, t := range p.Tools {
		if other.Allows(t) && !r.Allows(t) {
			r.Tools = append(r.Tools, t)
			if p.Approved(t) && other.Approved(t) {
				r.AutoApprove = append(r.AutoApprove, t)
			}
		}
	}
	return r
}

type Limits struct {
	MaxTurns    int   `json:"max_turns"`
	MaxTokens   int64 `json:"max_tokens"`
	MaxChildren int   `json:"max_children"`
	MaxDepth    int   `json:"max_depth"`
	MaxRepairs  int   `json:"max_repairs"`
	Deadline    int64 `json:"deadline"`
}

func DefaultLimits() Limits {
	return Limits{MaxTurns: 64, MaxTokens: 200000, MaxChildren: 16, MaxDepth: 4, MaxRepairs: 2}
}

type Budget struct {
	Turns    int   `json:"turns"`
	Tokens   int64 `json:"tokens"`
	Reserved int64 `json:"reserved"`
	Children int   `json:"children"`
}

type Node struct {
	ID           string       `json:"id"`
	Role         string       `json:"role"`
	State        string       `json:"state"`
	Attempt      int          `json:"attempt"`
	InvocationID InvocationID `json:"invocation_id,omitempty"`
	Result       string       `json:"result,omitempty"`
}

type Run struct {
	ID              RunID           `json:"id"`
	TaskID          TaskID          `json:"task_id"`
	TaskVersion     int             `json:"task_version"`
	SessionID       SessionID       `json:"session_id,omitempty"`
	RootRunID       RunID           `json:"root_run_id"`
	ParentRunID     RunID           `json:"parent_run_id,omitempty"`
	Depth           int             `json:"depth"`
	Workspace       string          `json:"workspace"`
	Mode            string          `json:"mode"`
	TemplateVersion int             `json:"template_version"`
	Model           model.Ref       `json:"model"`
	Policy          Policy          `json:"policy"`
	Limits          Limits          `json:"limits"`
	Budget          Budget          `json:"budget"`
	State           RunState        `json:"state"`
	Nodes           []Node          `json:"nodes"`
	Input           []model.Message `json:"input,omitempty"`
	InputFrozen     bool            `json:"input_frozen"`
	Prompt          string          `json:"prompt"`
	SystemPrompt    string          `json:"system_prompt,omitempty"`
	WaitingID       InputRequestID  `json:"waiting_id,omitempty"`
	Repairs         int             `json:"repairs"`
	Result          string          `json:"result,omitempty"`
	Error           string          `json:"error,omitempty"`
	Version         int64           `json:"version"`
	CreatedAt       int64           `json:"created_at"`
}

func (r *Run) Transition(to RunState) error {
	if !CanTransition(r.State, to) {
		return fmt.Errorf("illegal run transition %s -> %s", r.State, to)
	}
	r.State = to
	return nil
}

type Thread struct {
	ContextVersion int             `json:"context_version"`
	SequenceOffset int             `json:"sequence_offset"`
	Messages       []model.Message `json:"messages"`
	Cursor         *model.Cursor   `json:"cursor,omitempty"`
}

type Invocation struct {
	ID                InvocationID `json:"id"`
	RunID             RunID        `json:"run_id"`
	NodeID            string       `json:"node_id"`
	Attempt           int          `json:"attempt"`
	Role              string       `json:"role"`
	RoleVersion       int          `json:"role_version"`
	Model             model.Ref    `json:"model"`
	Policy            Policy       `json:"policy"`
	Thread            Thread       `json:"thread"`
	Phase             string       `json:"phase"`
	Pending           []model.Call `json:"pending,omitempty"`
	NextCall          int          `json:"next_call"`
	AssistantSequence int          `json:"assistant_sequence"`
	Reservation       int64        `json:"reservation"`
	Usage             model.Usage  `json:"usage"`
	Result            string       `json:"result,omitempty"`
	Error             string       `json:"error,omitempty"`
	Version           int64        `json:"version"`
}

type InputRequest struct {
	ID           InputRequestID `json:"id"`
	RunID        RunID          `json:"run_id"`
	InvocationID InvocationID   `json:"invocation_id"`
	Kind         string         `json:"kind"`
	CallKey      string         `json:"call_key,omitempty"`
	Prompt       string         `json:"prompt"`
	State        string         `json:"state"`
	Response     string         `json:"response,omitempty"`
	Approved     bool           `json:"approved"`
	ExpiresAt    int64          `json:"expires_at"`
	Version      int64          `json:"version"`
}

type ToolExecution struct {
	Key          string       `json:"key"`
	RunID        RunID        `json:"run_id"`
	InvocationID InvocationID `json:"invocation_id"`
	Name         string       `json:"name"`
	Arguments    string       `json:"arguments"`
	Workspace    string       `json:"workspace"`
	State        string       `json:"state"`
	Effect       string       `json:"effect"`
	Result       string       `json:"result,omitempty"`
	ExitCode     int          `json:"exit_code"`
	Version      int64        `json:"version"`
}

type Delegation struct {
	Key          string       `json:"key"`
	ParentRunID  RunID        `json:"parent_run_id"`
	InvocationID InvocationID `json:"invocation_id"`
	Children     []RunID      `json:"children"`
}

type Artifact struct {
	ID           int64        `json:"id"`
	RunID        RunID        `json:"run_id"`
	InvocationID InvocationID `json:"invocation_id"`
	Kind         string       `json:"kind"`
	Content      string       `json:"content"`
}

type Event struct {
	Sequence          int64        `json:"sequence"`
	RunID             RunID        `json:"run_id"`
	InvocationID      InvocationID `json:"invocation_id,omitempty"`
	AssistantSequence int          `json:"assistant_sequence,omitempty"`
	Kind              string       `json:"kind"`
	Content           string       `json:"content"`
}

var ErrConflict = errors.New("state version conflict")
var ErrNotFound = errors.New("record not found")
var ErrBudget = errors.New("execution budget exhausted")
var ErrUnknown = errors.New("external operation outcome unknown; reconciliation required")
var ErrCheckpoint = errors.New("checkpoint unavailable; execution stopped")

// Mutation is committed atomically; Version is the expected revision, with zero meaning insert.
type Mutation struct {
	Sessions    []*Session
	Tasks       []*Task
	Runs        []*Run
	Invocations []*Invocation
	Inputs      []*InputRequest
	Tools       []*ToolExecution
	Delegations []*Delegation
	Artifacts   []*Artifact
	Events      []Event
}
