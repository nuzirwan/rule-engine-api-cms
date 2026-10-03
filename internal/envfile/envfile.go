// Package envfile is a dependency-free loader for a tiny .env file (KEY=VALUE
// lines). It exists so cmd/engine can read local run config from a file without
// pulling a godotenv-style dependency into the module (stdlib + pgx + a valkey
// client only). Process environment wins: a key already set in the environment
// is never overridden by the file, so real env vars and CI secrets take
// precedence over the on-disk template.
package envfile

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// Load reads path if it exists and sets each KEY=VALUE line into the process
// environment via os.Setenv, but ONLY for keys not already present (os.LookupEnv
// reports unset) so the real environment overrides the file. A missing file is
// NOT an error (returns nil): the file is an optional convenience. Blank lines
// and lines whose first non-space rune is '#' are ignored. A value may be
// wrapped in matching single or double quotes, which are stripped. A line with
// no '=' is a malformed-config error naming the 1-based line number.
func Load(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // optional file: absence is fine
		}
		return fmt.Errorf("open env file %q: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	sc := bufio.NewScanner(f)
	line := 0
	for sc.Scan() {
		line++
		raw := strings.TrimSpace(sc.Text())
		if raw == "" || strings.HasPrefix(raw, "#") {
			continue
		}
		eq := strings.IndexByte(raw, '=')
		if eq < 0 {
			return fmt.Errorf("env file %q line %d: missing '=' in %q", path, line, raw)
		}
		key := strings.TrimSpace(raw[:eq])
		if key == "" {
			return fmt.Errorf("env file %q line %d: empty key", path, line)
		}
		val := unquote(strings.TrimSpace(raw[eq+1:]))
		if _, ok := os.LookupEnv(key); ok {
			continue // process env wins
		}
		if err := os.Setenv(key, val); err != nil {
			return fmt.Errorf("env file %q line %d: set %q: %w", path, line, key, err)
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("read env file %q: %w", path, err)
	}
	return nil
}

// unquote strips a single pair of matching surrounding single or double quotes.
// An unquoted value, or one with mismatched quotes, is returned unchanged.
func unquote(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}
