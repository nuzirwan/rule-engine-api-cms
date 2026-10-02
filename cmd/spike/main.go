// Command spike is the AC-S1 smoke proof: build zen-go via CGO, construct the
// engine, compile the representative JDM, and evaluate the three canonical cases.
// Run with: CGO_ENABLED=1 go run ./cmd/spike
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"nzr-rules-engine/internal/zenspike"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "AC-S1 FAILED:", err)
		os.Exit(1)
	}
}

func run() error {
	jdmPath := filepath.Join("internal", "zenspike", "testdata", "order.jdm.json")
	jdm, err := os.ReadFile(jdmPath)
	if err != nil {
		return fmt.Errorf("read jdm %s: %w", jdmPath, err)
	}

	eng := zenspike.NewEngine()
	defer eng.Dispose()

	compiled, err := eng.Compile(jdm)
	if err != nil {
		return err
	}
	defer compiled.Close()

	cases := []map[string]any{
		{"amount": 1500, "status": "paid"},    // expedited
		{"amount": 500, "status": "paid"},      // standard
		{"amount": 1500, "status": "pending"},  // standard
	}

	ctx := context.Background()
	for _, in := range cases {
		out, err := compiled.Eval(ctx, in)
		if err != nil {
			return err
		}
		fmt.Printf("input=%v => %v\n", in, out)
	}

	fmt.Println("AC-S1 OK")
	return nil
}
