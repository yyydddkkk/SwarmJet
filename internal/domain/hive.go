// internal/domain/hive.go
package domain

import (
	"errors"
	"time"
)

// Errors returned by the Hive store. Callers match them with errors.Is.
var (
	ErrNotFound        = errors.New("not found")
	ErrConflict        = errors.New("conflict")
	ErrHiveLimit       = errors.New("hive concurrent drone limit reached")
	ErrChainLimit      = errors.New("chain drone limit reached")
	ErrHopLimit        = errors.New("message hop limit reached")
	ErrPeerMessage     = errors.New("drones cannot message other drones")
	ErrMessageTooLarge = errors.New("message body too large")
)

const (
	MaxMessageBody = 8000 // characters
	MaxEventDigest = 2000 // characters
)

// Hive is one project: a GitHub repository with its Queen and Swarm.
type Hive struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	Repository   string     `json:"repository"` // owner/name
	QueenEnabled bool       `json:"queenEnabled"`
	Config       HiveConfig `json:"config"`
	Version      int        `json:"version"`
	CreatedAt    time.Time  `json:"createdAt"`
	UpdatedAt    time.Time  `json:"updatedAt"`
}

// HiveConfig holds per-Hive limits. Zero values mean "use the default".
type HiveConfig struct {
	CLI                   string        `json:"cli"`                   // default "opencode"
	MaxConcurrentDrones   int           `json:"maxConcurrentDrones"`   // default 4
	MaxHops               int           `json:"maxHops"`               // default 10
	MaxDronesPerChain     int           `json:"maxDronesPerChain"`     // default 10
	IdleParkMinutes       int           `json:"idleParkMinutes"`       // default 30
	HibernateHours        int           `json:"hibernateHours"`        // default 24
	TurnTimeoutMinutes    int           `json:"turnTimeoutMinutes"`    // default 120
	DroneMaxLifetimeHours int           `json:"droneMaxLifetimeHours"` // default 168
	Triggers              []TriggerRule `json:"triggers"`
}

// TriggerRule starts a Drone for a matching GitHub event.
type TriggerRule struct {
	Event         string `json:"event"`                   // "issue_labeled" or "issue_comment"
	Label         string `json:"label,omitempty"`         // for issue_labeled
	CommentPrefix string `json:"commentPrefix,omitempty"` // for issue_comment, e.g. "/swarmjet run"
	Preset        string `json:"preset,omitempty"`
}

type AgentRole string

const (
	RoleQueen AgentRole = "queen"
	RoleDrone AgentRole = "drone"
)

type AgentState string

const (
	AgentCreating   AgentState = "creating"
	AgentActive     AgentState = "active"
	AgentParked     AgentState = "parked"
	AgentHibernated AgentState = "hibernated"
	AgentCompleted  AgentState = "completed"
	AgentFailed     AgentState = "failed"
	AgentCancelled  AgentState = "cancelled"
	AgentDisabled   AgentState = "disabled" // Queen only: not enabled
)

// Agent is a Queen or a Drone.
type Agent struct {
	ID          string     `json:"id"`
	HiveID      string     `json:"hiveId"`
	Role        AgentRole  `json:"role"`
	State       AgentState `json:"state"`
	Preset      string     `json:"preset,omitempty"`
	CLI         string     `json:"cli"`
	Task        string     `json:"task,omitempty"` // Drone only
	Branch      string     `json:"branch,omitempty"`
	IssueNumber int64      `json:"issueNumber,omitempty"`
	PRNumber    int64      `json:"prNumber,omitempty"`
	NodeID      string     `json:"nodeId,omitempty"`
	ChainID     string     `json:"chainId,omitempty"`
	Handoff     *Handoff   `json:"handoff,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
	UpdatedAt   time.Time  `json:"updatedAt"`
}

// Handoff is the fixed-field summary a Drone updates after each turn.
type Handoff struct {
	Goal      string        `json:"goal"`
	Done      string        `json:"done"`
	Remaining string        `json:"remaining"`
	Risks     string        `json:"risks"`
	Next      string        `json:"next"`
	Artifacts []ArtifactRef `json:"artifacts,omitempty"`
}

type ArtifactRef struct {
	Kind string `json:"kind"` // branch, commit, pr, report, handoff
	Ref  string `json:"ref"`
}

type MessageKind string

const (
	MessageEvent    MessageKind = "event"
	MessageTask     MessageKind = "task"
	MessageReport   MessageKind = "report"
	MessageQuestion MessageKind = "question"
	MessageReply    MessageKind = "reply"
	MessageControl  MessageKind = "control"
)

// Address is "queen", "drone:<agentID>", "hive", "local:<tokenID>" or "system".
type Address string

const (
	AddressQueen  Address = "queen"
	AddressHive   Address = "hive"
	AddressSystem Address = "system"
)

func DroneAddress(id string) Address { return Address("drone:" + id) }
func LocalAddress(tokenID string) Address { return Address("local:" + tokenID) }

type MessageState string

const (
	MessagePending   MessageState = "pending"
	MessageDelivered MessageState = "delivered"
	MessageHandled   MessageState = "handled"
)

type Message struct {
	ID        string        `json:"id"` // idempotency key; empty means "generate"
	HiveID    string        `json:"hiveId"`
	From      Address       `json:"from"`
	To        Address       `json:"to"`
	Kind      MessageKind   `json:"kind"`
	ChainID   string        `json:"chainId"`
	Hop       int           `json:"hop"`
	Body      string        `json:"body"`
	Artifacts []ArtifactRef `json:"artifacts,omitempty"`
	State     MessageState  `json:"state"`
	CreatedAt time.Time     `json:"createdAt"`
}

type TokenScope string

const (
	TokenDefault TokenScope = "default" // spawn and view
	TokenAdmin   TokenScope = "admin"   // also configure Hives and credentials
)

type Token struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Scope     TokenScope `json:"scope"`
	CreatedAt time.Time  `json:"createdAt"`
}
