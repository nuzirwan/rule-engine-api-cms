package drivers

import (
	"strings"
	"testing"

	"nzr-rules-engine/internal/connect"
)

// Compile-time interface compliance assertions
var _ connect.Connector = mysqlConnector{}
var _ connect.Client = (*mysqlClient)(nil)

func TestMySQLConnectorType(t *testing.T) {
	c := newMySQLConnector()
	if got := c.Type(); got != "mysql" {
		t.Errorf("Type() = %q; want %q", got, "mysql")
	}
}

func TestMySQLConnectorLifecycle(t *testing.T) {
	c := newMySQLConnector()
	if got := c.Lifecycle(); got != connect.LifecyclePooled {
		t.Errorf("Lifecycle() = %v; want LifecyclePooled", got)
	}
}

func TestMySQLConnectorCapabilities(t *testing.T) {
	c := newMySQLConnector()
	if got := c.Capabilities(); got != connect.CapQueryExec {
		t.Errorf("Capabilities() = %v; want CapQueryExec", got)
	}
}

func TestBuildMySQLDSN(t *testing.T) {
	tests := []struct {
		name     string
		settings map[string]any
		wantDSN  string
	}{
		{
			name:     "explicit dsn passthrough",
			settings: map[string]any{"dsn": "custom:pass@tcp(db:3306)/mydb"},
			wantDSN:  "custom:pass@tcp(db:3306)/mydb",
		},
		{
			name:     "discrete settings compose",
			settings: map[string]any{"host": "dbhost", "port": "3307", "database": "testdb", "user": "admin"},
			wantDSN:  "admin@tcp(dbhost:3307)/testdb?parseTime=true",
		},
		{
			name:     "defaults applied",
			settings: map[string]any{"database": "app"},
			wantDSN:  "root@tcp(localhost:3306)/app?parseTime=true",
		},
		{
			name:     "host only",
			settings: map[string]any{"host": "myhost", "database": "db"},
			wantDSN:  "root@tcp(myhost:3306)/db?parseTime=true",
		},
		{
			name:     "port only",
			settings: map[string]any{"port": "3308", "database": "db"},
			wantDSN:  "root@tcp(localhost:3308)/db?parseTime=true",
		},
		{
			name:     "user only",
			settings: map[string]any{"user": "myuser", "database": "db"},
			wantDSN:  "myuser@tcp(localhost:3306)/db?parseTime=true",
		},
		{
			name:     "empty settings uses defaults",
			settings: map[string]any{},
			wantDSN:  "root@tcp(localhost:3306)/?parseTime=true",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			def := connect.ConnectionDef{Key: "test", Type: "mysql", Settings: tt.settings}
			got := buildMySQLDSN(def)
			if got != tt.wantDSN {
				t.Errorf("buildMySQLDSN() = %q; want %q", got, tt.wantDSN)
			}
		})
	}
}

func TestInjectMySQLPassword(t *testing.T) {
	tests := []struct {
		name     string
		dsn      string
		password string
		want     string
	}{
		{
			name:     "inject into simple dsn",
			dsn:      "root@tcp(localhost:3306)/mydb",
			password: "secret",
			want:     "root:secret@tcp(localhost:3306)/mydb",
		},
		{
			name:     "inject into dsn with options",
			dsn:      "admin@tcp(host:3306)/db?parseTime=true",
			password: "pass123",
			want:     "admin:pass123@tcp(host:3306)/db?parseTime=true",
		},
		{
			name:     "dsn without @ unchanged",
			dsn:      "localhost:3306",
			password: "secret",
			want:     "localhost:3306",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := injectMySQLPassword(tt.dsn, tt.password)
			if got != tt.want {
				t.Errorf("injectMySQLPassword() = %q; want %q", got, tt.want)
			}
		})
	}
}

func TestMySQLConnectorInAll(t *testing.T) {
	all := All()
	found := false
	for _, c := range all {
		if c.Type() == "mysql" {
			found = true
			break
		}
	}
	if !found {
		types := make([]string, len(all))
		for i, c := range all {
			types[i] = c.Type()
		}
		t.Errorf("mysql connector not found in All(); got types: %s", strings.Join(types, ", "))
	}
}
