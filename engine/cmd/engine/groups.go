package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"nzr-rules-engine/internal/config"
	"nzr-rules-engine/internal/gateway"
)

// groupsCmd handles the "groups" subcommand and its subcommands.
type groupsCmd struct {
	generateManifests *flag.FlagSet
	apply             *flag.FlagSet

	// generate-manifests flags
	genGroup     string
	genOutputDir string

	// apply flags
	applyGroup  string
	applyDryRun bool
}

// newGroupsCmd creates a new groupsCmd with subcommand flag sets.
func newGroupsCmd() *groupsCmd {
	cmd := &groupsCmd{
		generateManifests: flag.NewFlagSet("generate-manifests", flag.ExitOnError),
		apply:             flag.NewFlagSet("apply", flag.ExitOnError),
	}

	cmd.generateManifests.StringVar(&cmd.genGroup, "group", "", "specific group to generate manifests for (all groups if empty)")
	cmd.generateManifests.StringVar(&cmd.genOutputDir, "output-dir", "", "output directory for manifests (defaults to DISPATCH_MANIFEST_OUTPUT_DIR or ./k8s/workers)")

	cmd.apply.StringVar(&cmd.applyGroup, "group", "", "group to apply manifests for (required)")
	cmd.apply.BoolVar(&cmd.applyDryRun, "dry-run", false, "run kubectl apply --dry-run=client")

	return cmd
}

// run executes the groups subcommand.
func (c *groupsCmd) run(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: engine groups <generate-manifests|apply>\n\nSubcommands:\n  generate-manifests  Generate K8s manifests for worker groups\n  apply               Apply manifests via kubectl")
	}

	switch args[0] {
	case "generate-manifests":
		if err := c.generateManifests.Parse(args[1:]); err != nil {
			return err
		}
		return c.runGenerateManifests()
	case "apply":
		if err := c.apply.Parse(args[1:]); err != nil {
			return err
		}
		return c.runApply()
	default:
		return fmt.Errorf("unknown groups subcommand: %s", args[0])
	}
}

// runGenerateManifests generates K8s manifests for all groups or a specific group.
func (c *groupsCmd) runGenerateManifests() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Load dispatch config for defaults.
	dispatchCfg, err := config.LoadDispatchConfig()
	if err != nil {
		return fmt.Errorf("load dispatch config: %w", err)
	}

	// Determine output directory.
	outputDir := c.genOutputDir
	if outputDir == "" {
		outputDir = dispatchCfg.ManifestOutputDir
	}
	if outputDir == "" {
		outputDir = "./k8s/workers"
	}

	// Connect to config store.
	dsn := os.Getenv("CONFIG_DSN")
	if dsn == "" {
		return fmt.Errorf("CONFIG_DSN environment variable is required for manifest generation")
	}

	schema := os.Getenv("CONFIG_SCHEMA")
	if schema == "" {
		schema = "rule_engine"
	}

	pool, err := config.OpenSchemaPool(ctx, dsn, schema)
	if err != nil {
		return fmt.Errorf("connect to config store: %w", err)
	}
	defer pool.Close()

	store, err := config.NewPgStore(map[string]*pgxpool.Pool{"": pool})
	if err != nil {
		return fmt.Errorf("create config store: %w", err)
	}

	// Get groups to process.
	var groups []config.Group
	if c.genGroup != "" {
		g, err := store.GetGroup(ctx, "", c.genGroup)
		if err != nil {
			return fmt.Errorf("group %s not found: %w", c.genGroup, err)
		}
		groups = []config.Group{g}
	} else {
		summaries, err := store.ListGroups(ctx, "")
		if err != nil {
			return fmt.Errorf("list groups: %w", err)
		}
		// Load full group configs.
		for _, s := range summaries {
			g, err := store.GetGroup(ctx, "", s.ID)
			if err != nil {
				return fmt.Errorf("get group %s: %w", s.ID, err)
			}
			groups = append(groups, g)
		}
	}

	if len(groups) == 0 {
		fmt.Println("No groups found.")
		return nil
	}

	// Create manifest generator.
	gen, err := gateway.NewManifestGenerator()
	if err != nil {
		return fmt.Errorf("create manifest generator: %w", err)
	}

	// Generate manifests for each group.
	for _, g := range groups {
		if !g.Enabled {
			fmt.Printf("Skipping disabled group: %s\n", g.ID)
			continue
		}

		cfg := gateway.ManifestConfig{
			Group:          g.ID,
			Namespace:      dispatchCfg.Namespace,
			WorkerImage:    dispatchCfg.WorkerImage,
			ServiceAccount: dispatchCfg.ServiceAccount,
			MinReplicas:    g.Scaling.MinReplicas,
			MaxReplicas:    g.Scaling.MaxReplicas,
			ScaleDownDelay: parseScaleDownDelay(g.Scaling.ScaleDownDelay),
			PrometheusAddr: dispatchCfg.PrometheusAddress,
		}

		// Apply resource limits from group config.
		if g.Scaling.Resources != nil {
			if g.Scaling.Resources.CPURequest != "" {
				cfg.CPURequest = g.Scaling.Resources.CPURequest
			}
			if g.Scaling.Resources.CPULimit != "" {
				cfg.CPULimit = g.Scaling.Resources.CPULimit
			}
			if g.Scaling.Resources.MemoryRequest != "" {
				cfg.MemoryRequest = g.Scaling.Resources.MemoryRequest
			}
			if g.Scaling.Resources.MemoryLimit != "" {
				cfg.MemoryLimit = g.Scaling.Resources.MemoryLimit
			}
		}

		groupDir := filepath.Join(outputDir, g.ID)
		dynamicMode := g.Scaling.Mode == config.ScalingModeDynamic

		if err := gen.GenerateAll(cfg, groupDir, dynamicMode); err != nil {
			return fmt.Errorf("generate manifests for group %s: %w", g.ID, err)
		}

		fmt.Printf("Generated manifests for group %s in %s", g.ID, groupDir)
		if dynamicMode {
			fmt.Print(" (with KEDA ScaledObject)")
		}
		fmt.Println()
	}

	fmt.Printf("\nDone. Generated manifests in %s\n", outputDir)
	return nil
}

// runApply applies manifests for a group via kubectl.
func (c *groupsCmd) runApply() error {
	if c.applyGroup == "" {
		return fmt.Errorf("--group is required")
	}

	// Load dispatch config for manifest directory.
	dispatchCfg, _ := config.LoadDispatchConfig()
	outputDir := dispatchCfg.ManifestOutputDir
	if outputDir == "" {
		outputDir = "./k8s/workers"
	}

	groupDir := filepath.Join(outputDir, c.applyGroup)

	// Check if directory exists.
	if _, err := os.Stat(groupDir); os.IsNotExist(err) {
		return fmt.Errorf("manifest directory does not exist: %s (run generate-manifests first)", groupDir)
	}

	args := []string{"apply", "-f", groupDir}
	if c.applyDryRun {
		args = append(args, "--dry-run=client")
	}

	fmt.Printf("Running: kubectl %s\n", args)

	cmd := exec.Command("kubectl", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("kubectl apply failed: %w", err)
	}

	return nil
}

// parseScaleDownDelay parses a duration string like "5m" to seconds.
// Returns 300 (5 minutes) as default.
func parseScaleDownDelay(s string) int {
	if s == "" {
		return 300
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 300
	}
	return int(d.Seconds())
}
