# Cloudflare API Contract

**Feature**: Cloudflare Tunnel Docker Controller
**Interface**: Cloudflare Tunnel Ingress Rules API
**Version**: Cloudflare API v4

## Overview

This contract defines the interaction between DockTunnel and Cloudflare's Tunnel Ingress Rules API. All operations MUST respect rate limits (FR-030) and implement retry logic (FR-029).

---

## API Operations

### 1. Get Tunnel Configuration

**Purpose**: Fetch current ingress rules for comparison (FR-026)

**Endpoint**:
```
GET /accounts/{account_id}/cfd_tunnel/{tunnel_id}/configurations
```

**Request Parameters**:
| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| account_id | string | Yes | Cloudflare Account ID |
| tunnel_id | string | Yes | Tunnel UUID |

**Response** (200 OK):
```json
{
  "success": true,
  "result": {
    "config": {
      "ingress": [
        {
          "hostname": "app.example.com",
          "service": "http://172.17.0.5:8080",
          "path": "/api/*",
          "originRequest": {
            "connectTimeout": 30000000000,
            "noTLSVerify": true
          }
        },
        {
          "service": "http_status:404"
        }
      ]
    }
  }
}
```

**Error Responses**:
- `401 Unauthorized`: Invalid API token
- `403 Forbidden`: Insufficient permissions
- `404 Not Found`: Tunnel does not exist
- `429 Too Many Requests`: Rate limit exceeded (implement retry)
- `500 Server Error`: Cloudflare internal error (implement retry)

**Implementation Notes**:
- Catch-all rule (`http_status:404`) must always be last (FR-028)
- Response cached for 30 seconds to reduce API calls
- Called on startup and every reconciliation cycle (FR-023)

---

### 2. Update Tunnel Configuration

**Purpose**: Apply new ingress rules (FR-027)

**Endpoint**:
```
PUT /accounts/{account_id}/cfd_tunnel/{tunnel_id}/configurations
```

**Request Body**:
```json
{
  "config": {
    "ingress": [
      {
        "hostname": "app1.example.com",
        "service": "http://172.17.0.2:8080",
        "originRequest": {...}
      },
      {
        "hostname": "app2.example.com",
        "service": "http://172.17.0.3:3000",
        "path": "/api/*"
      },
      {
        "service": "http_status:404"
      }
    ]
  }
}
```

**Request Parameters**:
| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| account_id | string | Yes | Cloudflare Account ID |
| tunnel_id | string | Yes | Tunnel UUID |
| config | object | Yes | Full ingress configuration (replace, not merge) |

**Response** (200 OK):
```json
{
  "success": true,
  "result": {
    "config": {
      "ingress": [...]
    }
  }
}
```

**Error Responses**:
- `400 Bad Request`: Invalid configuration (e.g., duplicate hostnames)
- `401 Unauthorized`: Invalid API token
- `403 Forbidden`: Insufficient permissions
- `404 Not Found`: Tunnel does not exist
- `429 Too Many Requests`: Rate limit exceeded (implement retry)
- `500 Server Error`: Cloudflare internal error (implement retry)

**Implementation Notes**:
- **REPLACES** entire ingress array (not a merge operation)
- MUST include catch-all rule at the end
- Rate limited to 10 req/s using token bucket (FR-030)
- Implements exponential backoff retry up to 3 times (FR-029)
- Logs diff (before/after) at INFO level (FR-037)

**Retry Strategy**:
```
Attempt 1: Immediate
  └─ 429 Rate Limit → Wait 1s + jitter, retry
  └─ 5xx Server Error → Wait 1s + jitter, retry

Attempt 2: After backoff
  └─ 429 Rate Limit → Wait 2s + jitter, retry
  └─ 5xx Server Error → Wait 2s + jitter, retry

Attempt 3: After backoff
  └─ 429 Rate Limit → Wait 4s + jitter, retry
  └─ 5xx Server Error → Wait 4s + jitter, retry

Attempt 4: After backoff
  └─ Failure → Log ERROR, mark for manual reconciliation
```

---

### 3. Get Tunnel by Name

**Purpose**: Find existing tunnel ID by name (fallback for tunnel_id not provided)

**Endpoint**:
```
GET /accounts/{account_id}/cfd_tunnel
```

**Request Parameters**:
| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| account_id | string | Yes | Cloudflare Account ID |
| name | string | No (filter) | Tunnel name for filtering |

**Response** (200 OK):
```json
{
  "success": true,
  "result": [
    {
      "id": "abc123-def456-ghi789",
      "name": "DockTunnel",
      "tunnel_secret": "...",
      "created_at": "2026-01-03T10:00:00Z"
    }
  ]
}
```

**Implementation Notes**:
- Called on startup if `tunnel_id` not in config
- Returns first tunnel matching `tunnel_name` from config
- If no tunnel found, creates new tunnel (requires `tunnel_secret` generation)

---

### 4. Create Tunnel

**Purpose**: Create new Cloudflare Tunnel if none exists

**Endpoint**:
```
POST /accounts/{account_id}/cfd_tunnel
```

**Request Body**:
```json
{
  "name": "DockTunnel",
  "tunnel_secret": "random-32-byte-secret"
}
```

**Response** (201 Created):
```json
{
  "success": true,
  "result": {
    "id": "abc123-def456-ghi789",
    "name": "DockTunnel",
    "tunnel_secret": "...",
    "created_at": "2026-01-03T10:00:00Z"
  }
}
```

**Implementation Notes**:
- Only called if tunnel not found by name
- Secret generated via `crypto/rand` (32 bytes)
- New tunnel ID saved to config for future runs

---

## Data Type Mappings

### OriginRequest Settings

**Go Type** → **Cloudflare API JSON**:
```go
type OriginRequestConfig struct {
    NoTLSVerify          bool           `json:"noTLSVerify"`
    ConnectTimeout       time.Duration  `json:"connectTimeout"`       // Nanoseconds
    TLSTimeout           time.Duration  `json:"tlsTimeout"`           // Nanoseconds
    TCPKeepAlive         time.Duration  `json:"tcpKeepAlive"`         // Nanoseconds
    KeepAliveConnections int            `json:"keepAliveConnections"`
    KeepAliveTimeout     time.Duration  `json:"keepAliveTimeout"`     // Nanoseconds
    NoHappyEyeballs      bool           `json:"noHappyEyeballs"`
    ProxyType            string         `json:"proxyType"`
    ProxyAddress         string         `json:"proxyAddress"`
    ProxyPort            int            `json:"proxyPort"`
    HTTPHostHeader       string         `json:"httpHostHeader"`
    OriginServerName     string         `json:"originServerName"`
    MatchSNItoHost       bool           `json:"matchSnItoHost"`
    CAPool               string         `json:"caPool"`
    HTTP2Origin          bool           `json:"http2Origin"`
    DisableChunkedEncoding bool         `json:"disableChunkedEncoding"`
    Access               *AccessConfig  `json:"access,omitempty"`
}
```

**Duration Conversion**:
```go
// Cloudflare API expects nanoseconds
func (d *OriginRequestConfig) MarshalJSON() ([]byte, error) {
    type Alias OriginRequestConfig
    return json.Marshal(&struct {
        ConnectTimeout   int64 `json:"connectTimeout"`
        TLSTimeout       int64 `json:"tlsTimeout"`
        TCPKeepAlive     int64 `json:"tcpKeepAlive"`
        KeepAliveTimeout int64 `json:"keepAliveTimeout"`
        *Alias
    }{
        ConnectTimeout:   d.ConnectTimeout.Nanoseconds(),
        TLSTimeout:       d.TLSTimeout.Nanoseconds(),
        TCPKeepAlive:     d.TCPKeepAlive.Nanoseconds(),
        KeepAliveTimeout: d.KeepAliveTimeout.Nanoseconds(),
        Alias:            (*Alias)(d),
    })
}
```

---

## Error Handling Contract

### Retryable Errors (4xx, 5xx)

**429 Too Many Requests**:
```go
if apiErr.StatusCode == 429 {
    // Retry with exponential backoff
    // Log: "Rate limited, waiting {backoff} before retry"
    return retry
}
```

**5xx Server Errors**:
```go
if apiErr.StatusCode >= 500 && apiErr.StatusCode < 600 {
    // Retry with exponential backoff
    // Log: "Cloudflare server error, retrying"
    return retry
}
```

### Non-Retryable Errors

**400 Bad Request** (Validation Failure):
```go
if apiErr.StatusCode == 400 {
    // Log ERROR with full error details
    // Mark container as failed (FR-039)
    // Continue processing other containers
    return fmt.Errorf("invalid configuration: %w", apiErr)
}
```

**401 Unauthorized** (Authentication Failure):
```go
if apiErr.StatusCode == 401 {
    // Log CRITICAL error
    // Exit application (cannot proceed without auth)
    log.Fatal("Invalid Cloudflare API token")
}
```

**404 Not Found** (Tunnel Missing):
```go
if apiErr.StatusCode == 404 {
    // Attempt to create tunnel if not found
    // Log INFO: "Tunnel not found, creating new tunnel"
    return createTunnel()
}
```

---

## Rate Limiting Contract

### Token Bucket Algorithm (FR-030)

```go
import "golang.org/x/time/rate"

// Global rate limiter: 10 req/s, burst of 20
var apiLimiter = rate.NewLimiter(10, 20)

func callCloudflareAPI(ctx context.Context, apiCall func() error) error {
    // Wait until token available
    if err := apiLimiter.Wait(ctx); err != nil {
        return fmt.Errorf("rate limit wait failed: %w", err)
    }

    // Make API call
    return apiCall()
}
```

**Behavior**:
- Allows bursts up to 20 requests (e.g., during container start spike)
- Sustained rate limited to 10 req/s
- Blocks goroutine until token available
- Context cancellation propagates (graceful shutdown support)

---

## Testing Contract

### Mock Interface

```go
type CloudflareClient interface {
    GetTunnelConfig(ctx context.Context, accountID, tunnelID string) (*TunnelConfig, error)
    UpdateTunnelConfig(ctx context.Context, accountID, tunnelID string, config *TunnelConfig) error
    FindTunnelByName(ctx context.Context, accountID, name string) (*Tunnel, error)
    CreateTunnel(ctx context.Context, accountID, name string, secret []byte) (*Tunnel, error)
}
```

### Mock Implementation

```go
type MockCloudflareClient struct {
    mu           sync.Mutex
    tunnels      map[string]*Tunnel
    configs      map[string]*TunnelConfig
    callCount    int
    shouldFail   bool
    failAfter    int // Fail after N calls
}

func (m *MockCloudflareClient) UpdateTunnelConfig(ctx context.Context, accountID, tunnelID string, config *TunnelConfig) error {
    m.mu.Lock()
    defer m.mu.Unlock()

    m.callCount++

    if m.shouldFail && m.callCount > m.failAfter {
        return errors.New("mock API failure")
    }

    m.configs[tunnelID] = config
    return nil
}
```

**Test Scenarios**:
1. **Happy Path**: Config update succeeds
2. **Rate Limit**: Returns 429, client retries
3. **Server Error**: Returns 500, client retries
4. **Auth Failure**: Returns 401, exits fatally
5. **Validation Error**: Returns 400, logs error, continues

---

## Configuration Contract

### Required Configuration

```yaml
cloudflare:
  accountId: "your-cloudflare-account-id"  # Required
  apiToken: "your-api-token"                # Required
  tunnelName: "DockTunnel"                  # Required
  tunnelId: ""                              # Optional (auto-discovered if empty)
  rateLimit: 10                             # Optional (default: 10 req/s)
  maxRetries: 3                             # Optional (default: 3)
  retryDelay: 1s                            # Optional (default: 1s)
  maxRetryDelay: 30s                        # Optional (default: 30s)
```

**Validation** (FR-044):
```go
func validateCloudflareConfig(config *Config) error {
    if config.Cloudflare.AccountID == "" {
        return errors.New("cloudflare.accountId is required")
    }
    if config.Cloudflare.APIToken == "" {
        return errors.New("cloudflare.apiToken is required")
    }
    if config.Cloudflare.TunnelName == "" {
        return errors.New("cloudflare.tunnelName is required")
    }
    return nil
}
```

---

## Security Contract

### API Token Permissions

**Required Scopes** (Assumption #4):
- **Account**: Read + Write
- **Zone**: Read + Write (for DNS operations)
- **Tunnel**: Read + Write

**Token Validation**:
- Token validated on startup via test API call
- Invalid token causes immediate exit (cannot function)
- Token never logged or exposed in error messages

### TLS Configuration

All API communication over HTTPS:
```go
client := &http.Client{
    Transport: &http.Transport{
        TLSClientConfig: &tls.Config{
            MinVersion: tls.VersionTLS12,
        },
    },
}
```

---

## Monitoring & Observability

### Metrics Logged

| Metric | Level | Description |
|--------|-------|-------------|
| API Call Duration | DEBUG | How long each API call takes |
| Rate Limit Wait | WARN | When rate limiter blocks |
| Retry Attempt | INFO | When retrying failed API call |
| Config Diff | INFO | Before/after ingress rules |
| API Failure | ERROR | Non-retryable API errors |

**Example Log**:
```json
{
  "level": "INFO",
  "msg": "Cloudflare config updated",
  "tunnel_id": "abc123-def456",
  "ingress_count": 5,
  "duration_ms": 234,
  "added": ["app1.example.com"],
  "removed": ["app2.example.com"]
}
```

---

## References

- [Cloudflare Tunnel API Documentation](https://developers.cloudflare.com/api/resources/cloudflare_tunnel/subresources/ingress/rules/)
- [Cloudflare Rate Limits](https://developers.cloudflare.com/fundamentals/limits/rate-limits/)
