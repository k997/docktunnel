# Data Model & State Management

**Feature**: Cloudflare Tunnel Docker Controller
**Date**: 2026-01-03
**Phase**: 1 - Design & Contracts

## Entity Overview

This document defines the core data entities, their relationships, validation rules, and state transitions. All entities align with the functional requirements (FR-001 to FR-048) and constitution principles.

---

## 1. ContainerEvent

**Purpose**: Represents a Docker container lifecycle event triggering reconciliation

**Fields**:
| Field | Type | Description | Validation |
|-------|------|-------------|------------|
| ContainerID | string | Docker container ID (64-char hex) | Must match Docker regex `[a-f0-9]{64}` |
| ContainerName | string | Container name (e.g., `/my-app`) | Required, non-empty |
| EventType | EventType | Event type (`START`, `STOP`, `DIE`) | Required, enum |
| Timestamp | time.Time | When event occurred | Required, UTC |
| ActorAttributes | map[string]string | Raw container labels | Optional, may be empty |
| Labels | map[string]string | Parsed `docktunnel.*` labels | Populated post-parsing |

**State Transitions**: None (immutable event log entry)

**Relationships**:
- One-to-many with `TunnelEntry` (one container can have multiple services)

**Example**:
```go
ContainerEvent{
    ContainerID: "a1b2c3d4e5f6...",
    ContainerName: "/web-app",
    EventType: START,
    Timestamp: time.Now().UTC(),
    ActorAttributes: map[string]string{
        "docktunnel.enable": "true",
        "docktunnel.web.hostname": "app.example.com",
    },
}
```

---

## 2. TunnelConfiguration

**Purpose**: Represents a complete parsed configuration for a single service tunnel

**Fields**:
| Field | Type | Description | Validation |
|-------|------|-------------|------------|
| ServiceName | string | Label key suffix (e.g., `web`) | Required, matches `docktunnel.<name>.*` |
| ContainerID | string | Owner container ID | Required |
| Hostname | string | External hostname (e.g., `app.example.com`) | Required, FQDN format |
| Path | string | URL path prefix (e.g., `/api`) | Optional, starts with `/` |
| ServiceURL | string | Origin service URL (e.g., `http://10.0.0.1:8080`) | Required, valid URL |
| OriginRequest | OriginRequestConfig | Connection/security settings | Optional, defaults applied |
| Protocol | string | `http` or `https` | Required, lowercase |
| Port | int | Container port | Optional, 1-65535 |
| IPAddress | string | Container IP | Optional, validated if present |

**Validation Rules** (FR-004, FR-005):
- `Hostname` must be unique across all `TunnelConfiguration` instances
- `ServiceURL` must be valid URL format
- `Port` must be in range 1-65535 if specified

**Relationships**:
- One-to-one with `IngressRule`
- Many-to-one with `ContainerEvent` (multiple services per container)

**Example**:
```go
TunnelConfiguration{
    ServiceName: "web",
    ContainerID: "a1b2c3d4...",
    Hostname: "app.example.com",
    Path: "/api",
    ServiceURL: "http://172.17.0.5:8080",
    OriginRequest: OriginRequestConfig{
        ConnectTimeout: 30 * time.Second,
        NoTLSVerify: true,
    },
    Protocol: "http",
    Port: 8080,
    IPAddress: "172.17.0.5",
}
```

---

## 3. TunnelEntry

**Purpose**: Represents a tunnel route with lifecycle management and retention policy

**Fields**:
| Field | Type | Description | Validation |
|-------|------|-------------|------------|
| ContainerID | string | Owner container ID | Required, unique index |
| TunnelID | string | Cloudflare Tunnel UUID | Required, format `uuid-uuid-uuid` |
| ServiceName | string | Service identifier | Required |
| Config | TunnelConfiguration | Parsed configuration | Required |
| RetentionPolicy | RetentionPolicy | Deletion policy | Required |
| Status | EntryStatus | Lifecycle state | Required |
| CreatedAt | time.Time | Entry creation timestamp | Required, UTC |
| DeletedAt | *time.Time | Deletion timestamp | Optional, set when stopped |
| LastSyncAt | time.Time | Last Cloudflare sync | Required, UTC |

**Status Enum**:
```go
type EntryStatus int
const (
    StatusActive EntryStatus = iota  // Container running, route active
    StatusPendingDelete               // Container stopped, awaiting retention expiry
    StatusDeleted                     // Removed from Cloudflare
    StatusFlapping                    // In cooling period
)
```

**State Transitions**:
```
ACTIVE → PENDING_DELETE  (container stops, retention != immediate)
ACTIVE → DELETED         (container stops, retention == immediate)
PENDING_DELETE → ACTIVE  (container restarts within retention)
PENDING_DELETE → DELETED (retention expires)
ACTIVE → FLAPPING        (flapping detected)
FLAPPING → ACTIVE        (cooling period expires)
```

**Relationships**:
- One-to-one with `TunnelConfiguration`
- Many-to-one with `RetentionPolicy`

**Example**:
```go
TunnelEntry{
    ContainerID: "a1b2c3d4...",
    TunnelID: "abc123-def456-ghi789",
    ServiceName: "web",
    Config: TunnelConfiguration{...},
    RetentionPolicy: RetentionPolicy{
        Type: Timed,
        Duration: 30 * time.Minute,
    },
    Status: StatusActive,
    CreatedAt: time.Now().UTC(),
    LastSyncAt: time.Now().UTC(),
}
```

---

## 4. IngressRule

**Purpose**: Cloudflare Tunnel ingress rule (API format)

**Fields**:
| Field | Type | Description | Validation |
|-------|------|-------------|------------|
| Hostname | string | Matched hostname | Required |
| Path | string | Matched path | Optional, defaults to `/*` |
| Service | string | Origin service URL | Required |
| OriginRequest | OriginRequestConfig | Origin settings | Optional |

**Cloudflare API Mapping**:
```json
{
  "hostname": "app.example.com",
  "path": "/api/*",
  "service": "http://172.17.0.5:8080",
  "originRequest": {
    "connectTimeout": 30000000000,
    "noTLSVerify": true
  }
}
```

**Relationships**:
- One-to-one derived from `TunnelConfiguration`

---

## 5. RetentionPolicy

**Purpose**: Defines how long routes persist after container stops

**Fields**:
| Field | Type | Description | Validation |
|-------|------|-------------|------------|
| Type | PolicyType | Policy type | Required, enum |
| Duration | time.Duration | Time duration (for `Timed` type) | Required if `Type==Timed` |

**PolicyType Enum**:
```go
type PolicyType int
const (
    Immediate PolicyType = iota  // Delete immediately
    Timed                        // Delete after duration
    Forever                      // Never auto-delete
)
```

**Label Parsing** (FR-016):
- `"0"`, `"immediate"` → `Immediate`
- `"forever"`, `"keep"` → `Forever`
- `"30m"`, `"1h"`, `"7d"` → `Timed` (parsed by `time.ParseDuration`)

**Example**:
```go
// Immediate deletion
RetentionPolicy{Type: Immediate}

// 30-minute retention
RetentionPolicy{
    Type: Timed,
    Duration: 30 * time.Minute,
}

// Never delete
RetentionPolicy{Type: Forever}
```

---

## 6. OriginRequestConfig

**Purpose**: Connection and security settings for origin communication

**Fields**:
| Field | Type | Description | Default | Source |
|-------|------|-------------|---------|--------|
| NoTLSVerify | bool | Skip TLS verification | `false` | Label: `docktunnel.<name>.originRequest.noTLSVerify` |
| ConnectTimeout | time.Duration | TCP connect timeout | `30s` | Label: `docktunnel.<name>.originRequest.connectTimeout` |
| TLSTimeout | time.Duration | TLS handshake timeout | `10s` | Label: `docktunnel.<name>.originRequest.tlsTimeout` |
| TCPKeepAlive | time.Duration | TCP keep-alive interval | `30s` | Label: `docktunnel.<name>.originRequest.tcpKeepAlive` |
| KeepAliveConnections | int | Max idle connections | `100` | Label: `docktunnel.<name>.originRequest.keepAliveConnections` |
| KeepAliveTimeout | time.Duration | Idle connection timeout | `90s` | Label: `docktunnel.<name>.originRequest.keepAliveTimeout` |
| NoHappyEyeballs | bool | Disable IPv4/IPv6 fallback | `false` | Label: `docktunnel.<name>.originRequest.noHappyEyeballs` |
| ProxyType | string | Proxy type (`""` or `"socks"`) | `""` | Label: `docktunnel.<name>.originRequest.proxyType` |
| ProxyAddress | string | Proxy address | `""` | Label: `docktunnel.<name>.originRequest.proxyAddress` |
| ProxyPort | int | Proxy port | `0` | Label: `docktunnel.<name>.originRequest.proxyPort` |
| HTTPHostHeader | string | Override Host header | hostname | Label: `docktunnel.<name>.originRequest.httpHostHeader` |
| OriginServerName | string | SNI hostname | `""` | Label: `docktunnel.<name>.originRequest.originServerName` |
| MatchSNItoHost | bool | Auto-set SNI from hostname | `false` | Label: `docktunnel.<name>.originRequest.matchSniToHost` |
| CAPool | string | CA certificate path | `""` | Label: `docktunnel.<name>.originRequest.caPool` |
| HTTP2Origin | bool | Enable HTTP/2 | `false` | Label: `docktunnel.<name>.originRequest.http2Origin` |
| DisableChunkedEncoding | bool | Disable chunked encoding | `false` | Label: `docktunnel.<name>.originRequest.disableChunkedEncoding` |
| Access | *AccessConfig | Cloudflare Access settings | `nil` | Label: `docktunnel.<name>.originRequest.access.*` |

**AccessConfig Sub-structure**:
```go
type AccessConfig struct {
    Required bool   // true if access.required=true
    TeamName string // From access.teamName
    AudTag   string // From access.audTag
}
```

**Merge Strategy** (FR-010):
1. Start with global defaults (from config.yaml)
2. Override with container-specific labels
3. Labels always win over defaults

**Example**:
```go
OriginRequestConfig{
    NoTLSVerify: true,
    ConnectTimeout: 30 * time.Second,
    HTTP2Origin: true,
    Access: &AccessConfig{
        Required: true,
        TeamName: "my-team",
    },
}
```

---

## 7. StateSnapshot

**Purpose**: Persisted state for recovery after controller restart

**Fields**:
| Field | Type | Description |
|-------|------|-------------|
| Version | int | State format version |
| Timestamp | time.Time | Snapshot creation time |
| ActiveTunnels | map[string]*TunnelEntry | All active tunnel entries (key: containerID) |
| PendingDeletions | map[string]*TunnelEntry | Awaiting retention expiry (key: containerID) |
| FlappingContainers | map[string]FlappingState | Flapping detection state (key: containerID) |

**FlappingState Sub-structure**:
```go
type FlappingState struct {
    Transitions  []time.Time  // State transition timestamps
    LastFlapped  time.Time    // When flapping was detected
    CoolingUntil time.Time    // When cooling period ends
}
```

**Persistence** (FR-020):
- Encoded with gob (binary) for performance
- Fallback to JSON for debugging
- Atomic write (tmp file + rename)
- Loaded on startup before event processing

**Example**:
```go
StateSnapshot{
    Version: 1,
    Timestamp: time.Now().UTC(),
    ActiveTunnels: map[string]*TunnelEntry{
        "a1b2c3d4...": &TunnelEntry{...},
    },
    PendingDeletions: map[string]*TunnelEntry{
        "e5f6g7h8...": &TunnelEntry{
            DeletedAt: timePtr(time.Now().Add(-20 * time.Minute)),
            RetentionPolicy: RetentionPolicy{
                Type: Timed,
                Duration: 30 * time.Minute,
            },
        },
    },
    FlappingContainers: map[string]FlappingState{
        "i9j0k1l2...": FlappingState{
            CoolingUntil: time.Now().Add(60 * time.Second),
        },
    },
}
```

---

## State Machine Diagrams

### Container Lifecycle

```
          CONTAINER START
                 │
                 ▼
    ┌────────────────────────┐
    │   Parse Labels         │
    │   Validate Config      │
    └────────────────────────┘
                 │
                 ▼
      ┌──────────────────────┐
      │ Create TunnelEntry   │
      │ (Status: Active)     │
      └──────────────────────┘
                 │
                 ▼
    ┌────────────────────────┐
    │ Sync to Cloudflare API │
    └────────────────────────┘
                 │
                 ▼
          CONTAINER RUNNING
                 │
                 │ CONTAINER STOP
                 ▼
    ┌────────────────────────┐
    │ Apply Retention Policy │
    └────────────────────────┘
                 │
        ┌────────┴────────┐
        │                 │
   Immediate         Timed/Forever
        │                 │
        ▼                 ▼
  ┌─────────┐   ┌─────────────────┐
  │ DELETED │   │ PENDING_DELETE  │
  └─────────┘   └─────────────────┘
                     │
        ┌────────────┼────────────┐
        │            │            │
   Retention    Container    Retention
    Expires      Restarts     Expires
        │            │            │
        ▼            │            ▼
    ┌───────┐        │      ┌─────────┐
    │DELETED│        │      │ DELETED │
    └───────┘        │      └─────────┘
                     │
                     ▼
              ┌──────────┐
              │  ACTIVE  │
              └──────────┘
```

### Flapping Detection

```
CONTAINER STATE CHANGES (within 60s window)
│
├─ Transition 1: START  ─┐
├─ Transition 2: STOP    │
├─ Transition 3: START   │ < 5 transitions → Normal processing
├─ Transition 4: STOP   ─┘
│
├─ Transition 5: START
│  │
│  └──> TRIGGER FLAPPING (5th transition)
│       │
│       ▼
│  ┌─────────────────┐
│  │ Mark as FLAPPING│
│  │ Start 300s      │
│  │ cooling period  │
│  └─────────────────┘
│       │
│       ▼
│  ┌──────────────────┐
│  │ Ignore events    │
│  │ Log warnings     │
│  └──────────────────┘
│       │
│       ▼
│  Cooling period expires (300s)
│       │
│       ▼
│  ┌─────────────────┐
│  │ Return to ACTIVE│
│  │ Reset counter   │
│  └─────────────────┘
```

---

## Validation Rules Summary

### Hostname Uniqueness (FR-005)
```go
func validateHostnameUniqueness(
    newConfig TunnelConfiguration,
    existingConfigs []TunnelConfiguration,
) error {
    for _, existing := range existingConfigs {
        if existing.Hostname == newConfig.Hostname {
            return fmt.Errorf("duplicate hostname '%s': conflicts with container %s",
                newConfig.Hostname, existing.ContainerID)
        }
    }
    return nil
}
```

### Required Fields (FR-004)
```go
func validateRequiredFields(config TunnelConfiguration) error {
    if config.Hostname == "" {
        return errors.New("hostname is required")
    }
    if config.ServiceURL == "" {
        return errors.New("service URL is required")
    }
    if _, err := url.Parse(config.ServiceURL); err != nil {
        return fmt.Errorf("invalid service URL: %w", err)
    }
    return nil
}
```

---

## Indexes and Performance

### Primary Indexes
- `TunnelEntry.ContainerID` → O(1) lookup
- `TunnelEntry.Config.Hostname` → O(n) uniqueness validation

### Secondary Indexes
- `TunnelEntry.Status` → List active/pending deleted
- `TunnelEntry.DeletedAt` → GC scan (timestamp comparison)

### Memory Estimation (100 containers, 3 services each)
```
TunnelEntry: ~500 bytes × 300 = 150 KB
TunnelConfiguration: ~300 bytes × 300 = 90 KB
StateSnapshot: ~1 MB total
Overhead: ~5 MB (maps, pointers, etc.)
──────────────────────────────────────
Total: ~6.5 MB (well under 100 MB limit, SC-009)
```

---

## Concurrency Safety

All state maps use `sync.RWMutex`:
```go
type StateManager struct {
    mu             sync.RWMutex
    activeTunnels  map[string]*TunnelEntry
    pendingDeletes map[string]*TunnelEntry
}

func (sm *StateManager) Add(entry *TunnelEntry) {
    sm.mu.Lock()
    defer sm.mu.Unlock()
    sm.activeTunnels[entry.ContainerID] = entry
}

func (sm *StateManager) Get(containerID string) (*TunnelEntry, bool) {
    sm.mu.RLock()
    defer sm.mu.RUnlock()
    entry, ok := sm.activeTunnels[containerID]
    return entry, ok
}
```

**Alignment**: Constitution Principle I (Event Processing) - mutex ensures no race conditions during concurrent event processing
