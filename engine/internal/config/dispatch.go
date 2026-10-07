package config

import (
	"os"
	"time"

	// k8s.io/client-go imported for endpoint watching in gateway mode.
	// This blank import ensures the dependency stays in go.mod for later features.
	_ "k8s.io/client-go/kubernetes"
)

// DispatchMode defines how the gateway routes requests to workers.
type DispatchMode string

const (
	// DispatchInline processes requests in the same process (no worker pods).
	DispatchInline DispatchMode = "inline"
	// DispatchGateway routes requests to external worker pods via HTTP.
	DispatchGateway DispatchMode = "gateway"
)

// validDispatchModes is the set of allowed dispatch modes for validation.
var validDispatchModes = map[DispatchMode]bool{
	DispatchInline:  true,
	DispatchGateway: true,
}

// DispatchConfig holds the gateway dispatcher configuration loaded from env vars.
type DispatchConfig struct {
	// Mode is the dispatch mode: inline (in-process) or gateway (external workers).
	Mode DispatchMode
	// WorkerImage is the container image for spawned worker pods (gateway mode).
	WorkerImage string
	// Namespace is the Kubernetes namespace for worker pods.
	Namespace string
	// ServiceAccount is the K8s service account for worker pods.
	ServiceAccount string
	// DefaultGroup is the fallback worker group when a flow has no explicit group.
	DefaultGroup string
	// StartupTimeout is how long to wait for a worker pod to become ready.
	StartupTimeout time.Duration
	// RequestTimeout is how long to wait for a worker to process a request.
	RequestTimeout time.Duration
}

// LoadDispatchConfig reads dispatch configuration from environment variables.
// Returns a Validation-class error if DISPATCH_MODE is invalid.
func LoadDispatchConfig() (*DispatchConfig, error) {
	mode := DispatchMode(getEnvOrDefault("DISPATCH_MODE", string(DispatchInline)))
	if !validDispatchModes[mode] {
		return nil, newErr(Validation, "DISPATCH_MODE must be one of: inline, gateway")
	}

	startupTimeout, err := parseDurationOrDefault("DISPATCH_STARTUP_TIMEOUT", 30*time.Second)
	if err != nil {
		return nil, wrapErr(Validation, "invalid DISPATCH_STARTUP_TIMEOUT", err)
	}

	requestTimeout, err := parseDurationOrDefault("DISPATCH_REQUEST_TIMEOUT", 60*time.Second)
	if err != nil {
		return nil, wrapErr(Validation, "invalid DISPATCH_REQUEST_TIMEOUT", err)
	}

	return &DispatchConfig{
		Mode:           mode,
		WorkerImage:    os.Getenv("DISPATCH_WORKER_IMAGE"),
		Namespace:      getEnvOrDefault("DISPATCH_WORKER_NAMESPACE", "flow-workers"),
		ServiceAccount: os.Getenv("DISPATCH_SERVICE_ACCOUNT"),
		DefaultGroup:   getEnvOrDefault("DISPATCH_DEFAULT_GROUP", "default"),
		StartupTimeout: startupTimeout,
		RequestTimeout: requestTimeout,
	}, nil
}

// getEnvOrDefault returns the env var value or the default if unset/empty.
func getEnvOrDefault(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

// parseDurationOrDefault parses the env var as a duration or returns the default.
func parseDurationOrDefault(key string, defaultVal time.Duration) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return defaultVal, nil
	}
	return time.ParseDuration(v)
}
