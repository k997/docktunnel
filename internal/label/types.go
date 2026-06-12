package label

import "time"

// --- Minimal Traefik types (HTTP + TCP only) ---

// Configuration is the root Traefik label configuration, stripped to HTTP+TCP only.
type Configuration struct {
	HTTP *HTTPConfiguration
	TCP  *TCPConfiguration
}

type HTTPConfiguration struct {
	Routers  map[string]*Router
	Services map[string]*Service
}

type Router struct {
	Rule    string
	Service string
}

type Service struct {
	LoadBalancer *ServersLoadBalancer
}

type ServersLoadBalancer struct {
	Servers []Server
}

type Server struct {
	URL    string
	Scheme string
	Port   string
}

type TCPConfiguration struct {
	Routers  map[string]*TCPRouter
	Services map[string]*TCPService
}

type TCPRouter struct {
	Rule    string
	Service string
}

type TCPService struct {
	LoadBalancer *TCPServersLoadBalancer
}

type TCPServersLoadBalancer struct {
	Servers []TCPServer
}

type TCPServer struct {
	Port   string
	Scheme string
}

// --- DockTunnel intermediate types ---

// ServiceConfig holds decoded docktunnel.* label values for a single service.
// All values are strings; type conversion happens in the build stage.
type ServiceConfig struct {
	Hostname  string
	Service   string
	Port      string
	Scheme    string
	Proto     string
	Path      string
	Retention string

	// OriginRequest fields (string values, converted in builder)
	ConnectTimeout         string
	TLSTimeout             string
	TCPKeepAlive           string
	KeepAliveConnections   string
	KeepAliveTimeout      string
	NoHappyEyeballs        string
	NoTLSVerify           string
	HTTP2Origin           string
	DisableChunkedEncoding string
	HTTPHostHeader         string
	OriginServerName      string
	CAPool                string
	ProxyType             string
	ProxyAddress          string
	ProxyPort             string
	MatchSNItoHost        string

	// Access fields
	AccessRequired string
	AccessTeamName string
	AccessAUDTag   string
}

// IngressSpec is the unified intermediate representation for both
// docktunnel and traefik decoded results, before CF Ingress conversion.
type IngressSpec struct {
	Hostname      string
	Path          string
	Port          int
	Scheme        string
	ServiceURL    string
	OriginRequest *OriginRequestSpec
	Retention     string
}

// OriginRequestSpec holds converted origin request values.
// Pointer types distinguish "not set" from zero value.
type OriginRequestSpec struct {
	ConnectTimeout         *time.Duration
	TLSTimeout             *time.Duration
	TCPKeepAlive           *time.Duration
	KeepAliveConnections  *int64
	KeepAliveTimeout      *time.Duration
	NoHappyEyeballs       *bool
	NoTLSVerify           *bool
	OriginServerName      string
	CAPool                string
	HTTP2Origin           *bool
	HTTPHostHeader        string
	DisableChunkedEncoding *bool
	ProxyType             string
	ProxyAddress          string
	ProxyPort             *int
	MatchSNItoHost        *bool
	Access                *AccessSpec
}

// AccessSpec holds Cloudflare Access configuration.
type AccessSpec struct {
	Required bool
	TeamName string
	AUDTag   []string
}
