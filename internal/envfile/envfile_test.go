package envfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMissingFileIsNil(t *testing.T) {
	// A missing file is not an error and sets nothing.
	key := "ENVFILE_TEST_MISSING_KEY"
	_ = os.Unsetenv(key)
	if err := Load(filepath.Join(t.TempDir(), "does-not-exist.env")); err != nil {
		t.Fatalf("missing file should be nil, got %v", err)
	}
	if _, ok := os.LookupEnv(key); ok {
		t.Fatalf("missing-file load must set nothing")
	}
}

func TestLoadParsesCommentsBlanksAndQuotes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	content := "" +
		"# a comment\n" +
		"\n" +
		"   # indented comment\n" +
		"PLAIN=value1\n" +
		"  SPACED  =  value2  \n" +
		"DQUOTED=\"quoted value\"\n" +
		"SQUOTED='single quoted'\n" +
		"EMPTY=\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	for _, k := range []string{"PLAIN", "SPACED", "DQUOTED", "SQUOTED", "EMPTY"} {
		_ = os.Unsetenv(k)
		t.Cleanup(func() { _ = os.Unsetenv(k) })
	}

	if err := Load(path); err != nil {
		t.Fatalf("load: %v", err)
	}
	cases := map[string]string{
		"PLAIN":   "value1",
		"SPACED":  "value2",
		"DQUOTED": "quoted value",
		"SQUOTED": "single quoted",
		"EMPTY":   "",
	}
	for k, want := range cases {
		if got := os.Getenv(k); got != want {
			t.Errorf("%s = %q; want %q", k, got, want)
		}
	}
}

func TestLoadDoesNotOverrideProcessEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte("PRESET=fromfile\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Setenv("PRESET", "fromenv")

	if err := Load(path); err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := os.Getenv("PRESET"); got != "fromenv" {
		t.Fatalf("process env must win: PRESET = %q; want fromenv", got)
	}
}

func TestLoadMalformedLineErrors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte("GOOD=1\nNOEQUALS\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Cleanup(func() { _ = os.Unsetenv("GOOD") })

	err := Load(path)
	if err == nil {
		t.Fatal("expected an error for a line with no '='")
	}
}

// TestLoadExampleFile parses the committed .env.example to prove it is a valid
// KEY=VALUE document (the file the user copies to .env).
func TestLoadExampleFile(t *testing.T) {
	// repo root is two levels up from internal/envfile.
	path := filepath.Join("..", "..", ".env.example")
	if _, err := os.Stat(path); err != nil {
		t.Skipf(".env.example not found at %s: %v", path, err)
	}
	// Load into a throwaway process-env snapshot: pre-set every key so Load does
	// not pollute the test process environment, while still exercising the parse.
	// (Load's "process env wins" rule means pre-set keys are left untouched.)
	for _, k := range []string{
		"ORDERS_PG_DSN", "SHIP_REST_BASE_URL", "ENGINE_ADDR", "RUN_REST_STUB",
		"REST_STUB_PORT", "CONFIG_DSN", "CONFIG_SCHEMA", "VALKEY_ADDR",
	} {
		t.Setenv(k, "__preset__")
	}
	if err := Load(path); err != nil {
		t.Fatalf("example env file must parse: %v", err)
	}
}
