package types

import "time"

// EventType represents Docker container lifecycle event types
type EventType int

const (
	START EventType = iota
	STOP
	DIE
)

// EntryStatus represents the lifecycle status of a tunnel entry
type EntryStatus int

const (
	StatusActive        EntryStatus = iota // Container running, route active
	StatusPendingDelete                    // Immediate or expired, awaiting cleanup
	StatusRetaining                        // Container stopped, retention in progress
	StatusDeleted                          // Removed from Cloudflare
	StatusFlapping                         // In cooling period
)

// PolicyType represents retention policy types
type PolicyType int

const (
	Immediate PolicyType = iota // Delete immediately
	Timed                       // Delete after duration
	Forever                     // Never auto-delete
)

// ContainerEvent represents a Docker container lifecycle event triggering reconciliation
type ContainerEvent struct {
	ContainerID     string
	ContainerName   string
	EventType       EventType
	Timestamp       time.Time
	ActorAttributes map[string]string
	Labels          map[string]string
}

// TunnelConfiguration represents a complete parsed configuration for a single service tunnel
type TunnelConfiguration struct {
	ServiceName   string
	ContainerID   string
	Hostname      string
	Path          string
	ServiceURL    string
	OriginRequest OriginRequestConfig
	Protocol      string
	Port          int
	IPAddress     string
}

// OriginRequestConfig represents connection and security settings for origin communication
type OriginRequestConfig struct {
	NoTLSVerify            bool          `json:"noTLSVerify"`
	ConnectTimeout         time.Duration `json:"connectTimeout"`
	TLSTimeout             time.Duration `json:"tlsTimeout"`
	TCPKeepAlive           time.Duration `json:"tcpKeepAlive"`
	KeepAliveConnections   int           `json:"keepAliveConnections"`
	KeepAliveTimeout       time.Duration `json:"keepAliveTimeout"`
	NoHappyEyeballs        bool          `json:"noHappyEyeballs"`
	ProxyType              string        `json:"proxyType"`
	ProxyAddress           string        `json:"proxyAddress"`
	ProxyPort              int           `json:"proxyPort"`
	HTTPHostHeader         string        `json:"httpHostHeader"`
	OriginServerName       string        `json:"originServerName"`
	MatchSNItoHost         bool          `json:"matchSnItoHost"`
	CAPool                 string        `json:"caPool"`
	HTTP2Origin            bool          `json:"http2Origin"`
	DisableChunkedEncoding bool          `json:"disableChunkedEncoding"`
	Access                 *AccessConfig `json:"access,omitempty"`
}

// AccessConfig represents Cloudflare Access configuration
type AccessConfig struct {
	Required bool   `json:"required"`
	TeamName string `json:"teamName"`
	AudTag   string `json:"audTag"`
}

// TunnelEntry represents a tunnel route with lifecycle management and retention policy
type TunnelEntry struct {
	ContainerID     string
	TunnelID        string
	ServiceName     string
	Config          TunnelConfiguration
	RetentionPolicy RetentionPolicy
	Status          EntryStatus
	CreatedAt       time.Time
	DeletedAt       *time.Time
	LastSyncAt      time.Time
}

// RetentionPolicy represents how long a tunnel route should persist after container stops
type RetentionPolicy struct {
	Type     PolicyType
	Duration time.Duration // For Timed type
}

// FlappingState tracks flapping detection state for a container
type FlappingState struct {
	Transitions  []time.Time
	LastFlapped  time.Time
	CoolingUntil time.Time
}

// TransitionEvent represents lifecycle events that trigger state transitions
type TransitionEvent int

const (
	EventContainerStarted TransitionEvent = iota
	EventContainerStopped
	EventRetentionExpired
	EventCleanupComplete
)

// ActionKind represents the type of action the controller should perform after a transition
type ActionKind int

const (
	ActionNone        ActionKind = iota
	ActionDeleteRoute            // Delete ingress rule and DNS for hostname
)

// Action represents an action the controller should perform after a state transition
type Action struct {
	Kind        ActionKind
	ContainerID string
	ServiceName string
	Hostname    string
}

// CompensationRecord represents a failed action awaiting retry.
type CompensationRecord struct {
	ID          string // unique record ID
	Action      Action // the action to retry
	RetryCount  int
	MaxRetries  int
	NextRetryAt time.Time
	LastError   string
	CreatedAt   time.Time
	Dead        bool // true if permanently failed or retries exhausted
}

// StateSnapshot represents the persisted state of the controller for recovery after restart
type StateSnapshot struct {
	Version            int
	Timestamp          time.Time
	ActiveTunnels      map[string]*TunnelEntry
	PendingDeletions   map[string]*TunnelEntry
	FlappingContainers map[string]FlappingState
	PendingActions     map[string]*CompensationRecord
}
