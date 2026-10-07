package config

import (
	"errors"
	"os"
	"testing"
	"time"
)

// TestDispatchModeDefaults proves LoadDispatchConfig returns inline mode by default
// when DISPATCH_MODE is unset.
func TestDispatchModeDefaults(t *testing.T) {
	clearDispatchEnv(t)

	cfg, err := LoadDispatchConfig()
	if err != nil {
		t.Fatalf("LoadDispatchConfig() error = %v; want nil", err)
	}
	if cfg.Mode != DispatchInline {
		t.Errorf("Mode = %q; want %q", cfg.Mode, DispatchInline)
	}
}

// TestDispatchConfigDefaults proves all default values are applied correctly.
func TestDispatchConfigDefaults(t *testing.T) {
	clearDispatchEnv(t)

	cfg, err := LoadDispatchConfig()
	if err != nil {
		t.Fatalf("LoadDispatchConfig() error = %v; want nil", err)
	}

	if cfg.Mode != DispatchInline {
		t.Errorf("Mode = %q; want %q", cfg.Mode, DispatchInline)
	}
	if cfg.WorkerImage != "" {
		t.Errorf("WorkerImage = %q; want empty", cfg.WorkerImage)
	}
	if cfg.Namespace != "flow-workers" {
		t.Errorf("Namespace = %q; want %q", cfg.Namespace, "flow-workers")
	}
	if cfg.ServiceAccount != "" {
		t.Errorf("ServiceAccount = %q; want empty", cfg.ServiceAccount)
	}
	if cfg.DefaultGroup != "default" {
		t.Errorf("DefaultGroup = %q; want %q", cfg.DefaultGroup, "default")
	}
	if cfg.StartupTimeout != 30*time.Second {
		t.Errorf("StartupTimeout = %v; want %v", cfg.StartupTimeout, 30*time.Second)
	}
	if cfg.RequestTimeout != 60*time.Second {
		t.Errorf("RequestTimeout = %v; want %v", cfg.RequestTimeout, 60*time.Second)
	}
}

// TestDispatchConfigParsesAllEnvVars proves LoadDispatchConfig parses all env vars.
func TestDispatchConfigParsesAllEnvVars(t *testing.T) {
	clearDispatchEnv(t)

	t.Setenv("DISPATCH_MODE", "gateway")
	t.Setenv("DISPATCH_WORKER_IMAGE", "myrepo/worker:v1")
	t.Setenv("DISPATCH_WORKER_NAMESPACE", "custom-ns")
	t.Setenv("DISPATCH_SERVICE_ACCOUNT", "worker-sa")
	t.Setenv("DISPATCH_DEFAULT_GROUP", "priority")
	t.Setenv("DISPATCH_STARTUP_TIMEOUT", "45s")
	t.Setenv("DISPATCH_REQUEST_TIMEOUT", "120s")

	cfg, err := LoadDispatchConfig()
	if err != nil {
		t.Fatalf("LoadDispatchConfig() error = %v; want nil", err)
	}

	if cfg.Mode != DispatchGateway {
		t.Errorf("Mode = %q; want %q", cfg.Mode, DispatchGateway)
	}
	if cfg.WorkerImage != "myrepo/worker:v1" {
		t.Errorf("WorkerImage = %q; want %q", cfg.WorkerImage, "myrepo/worker:v1")
	}
	if cfg.Namespace != "custom-ns" {
		t.Errorf("Namespace = %q; want %q", cfg.Namespace, "custom-ns")
	}
	if cfg.ServiceAccount != "worker-sa" {
		t.Errorf("ServiceAccount = %q; want %q", cfg.ServiceAccount, "worker-sa")
	}
	if cfg.DefaultGroup != "priority" {
		t.Errorf("DefaultGroup = %q; want %q", cfg.DefaultGroup, "priority")
	}
	if cfg.StartupTimeout != 45*time.Second {
		t.Errorf("StartupTimeout = %v; want %v", cfg.StartupTimeout, 45*time.Second)
	}
	if cfg.RequestTimeout != 120*time.Second {
		t.Errorf("RequestTimeout = %v; want %v", cfg.RequestTimeout, 120*time.Second)
	}
}

// TestDispatchModeInvalid proves an invalid DISPATCH_MODE returns a Validation error.
func TestDispatchModeInvalid(t *testing.T) {
	clearDispatchEnv(t)
	t.Setenv("DISPATCH_MODE", "invalid-mode")

	cfg, err := LoadDispatchConfig()
	if cfg != nil {
		t.Errorf("LoadDispatchConfig() cfg = %v; want nil", cfg)
	}
	if err == nil {
		t.Fatal("LoadDispatchConfig() error = nil; want Validation error")
	}
	if !errors.Is(err, ErrValidation) {
		t.Errorf("error type = %T; want errors.Is(err, ErrValidation)", err)
	}
}

// TestDispatchTimeoutParsesDurationStrings proves timeout env vars accept duration strings.
func TestDispatchTimeoutParsesDurationStrings(t *testing.T) {
	cases := []struct {
		name         string
		startupEnv   string
		requestEnv   string
		wantStartup  time.Duration
		wantRequest  time.Duration
	}{
		{
			name:         "milliseconds",
			startupEnv:   "500ms",
			requestEnv:   "1500ms",
			wantStartup:  500 * time.Millisecond,
			wantRequest:  1500 * time.Millisecond,
		},
		{
			name:         "minutes",
			startupEnv:   "2m",
			requestEnv:   "5m",
			wantStartup:  2 * time.Minute,
			wantRequest:  5 * time.Minute,
		},
		{
			name:         "mixed",
			startupEnv:   "1m30s",
			requestEnv:   "2m30s",
			wantStartup:  90 * time.Second,
			wantRequest:  150 * time.Second,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearDispatchEnv(t)
			t.Setenv("DISPATCH_STARTUP_TIMEOUT", tc.startupEnv)
			t.Setenv("DISPATCH_REQUEST_TIMEOUT", tc.requestEnv)

			cfg, err := LoadDispatchConfig()
			if err != nil {
				t.Fatalf("LoadDispatchConfig() error = %v; want nil", err)
			}
			if cfg.StartupTimeout != tc.wantStartup {
				t.Errorf("StartupTimeout = %v; want %v", cfg.StartupTimeout, tc.wantStartup)
			}
			if cfg.RequestTimeout != tc.wantRequest {
				t.Errorf("RequestTimeout = %v; want %v", cfg.RequestTimeout, tc.wantRequest)
			}
		})
	}
}

// TestDispatchInvalidStartupTimeout proves invalid DISPATCH_STARTUP_TIMEOUT returns error.
func TestDispatchInvalidStartupTimeout(t *testing.T) {
	clearDispatchEnv(t)
	t.Setenv("DISPATCH_STARTUP_TIMEOUT", "not-a-duration")

	cfg, err := LoadDispatchConfig()
	if cfg != nil {
		t.Errorf("LoadDispatchConfig() cfg = %v; want nil", cfg)
	}
	if err == nil {
		t.Fatal("LoadDispatchConfig() error = nil; want Validation error")
	}
	if !errors.Is(err, ErrValidation) {
		t.Errorf("error type = %T; want errors.Is(err, ErrValidation)", err)
	}
}

// TestDispatchInvalidRequestTimeout proves invalid DISPATCH_REQUEST_TIMEOUT returns error.
func TestDispatchInvalidRequestTimeout(t *testing.T) {
	clearDispatchEnv(t)
	t.Setenv("DISPATCH_REQUEST_TIMEOUT", "bad-value")

	cfg, err := LoadDispatchConfig()
	if cfg != nil {
		t.Errorf("LoadDispatchConfig() cfg = %v; want nil", cfg)
	}
	if err == nil {
		t.Fatal("LoadDispatchConfig() error = nil; want Validation error")
	}
	if !errors.Is(err, ErrValidation) {
		t.Errorf("error type = %T; want errors.Is(err, ErrValidation)", err)
	}
}

// clearDispatchEnv unsets all DISPATCH_* env vars for test isolation.
func clearDispatchEnv(t *testing.T) {
	t.Helper()
	envVars := []string{
		"DISPATCH_MODE",
		"DISPATCH_WORKER_IMAGE",
		"DISPATCH_WORKER_NAMESPACE",
		"DISPATCH_SERVICE_ACCOUNT",
		"DISPATCH_DEFAULT_GROUP",
		"DISPATCH_STARTUP_TIMEOUT",
		"DISPATCH_REQUEST_TIMEOUT",
	}
	for _, v := range envVars {
		os.Unsetenv(v)
	}
}
