package gateway

import (
	"bytes"
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"text/template"
)

//go:embed templates/*.tmpl
var templatesFS embed.FS

// ManifestConfig holds all values needed to generate manifests for a group.
type ManifestConfig struct {
	Group          string
	Namespace      string
	WorkerImage    string
	ServiceAccount string
	MinReplicas    int
	MaxReplicas    int
	ScaleDownDelay int    // seconds
	CPURequest     string
	CPULimit       string
	MemoryRequest  string
	MemoryLimit    string
	PrometheusAddr string // e.g. http://prometheus:9090
}

// ManifestGenerator generates K8s YAML manifests from group configuration using
// embedded Go templates.
type ManifestGenerator struct {
	deploymentTmpl   *template.Template
	serviceTmpl      *template.Template
	scaledObjectTmpl *template.Template
}

// NewManifestGenerator creates a ManifestGenerator by parsing the embedded templates.
func NewManifestGenerator() (*ManifestGenerator, error) {
	deploymentTmpl, err := template.ParseFS(templatesFS, "templates/deployment.yaml.tmpl")
	if err != nil {
		return nil, fmt.Errorf("parse deployment template: %w", err)
	}

	serviceTmpl, err := template.ParseFS(templatesFS, "templates/service.yaml.tmpl")
	if err != nil {
		return nil, fmt.Errorf("parse service template: %w", err)
	}

	scaledObjectTmpl, err := template.ParseFS(templatesFS, "templates/scaledobject.yaml.tmpl")
	if err != nil {
		return nil, fmt.Errorf("parse scaledobject template: %w", err)
	}

	return &ManifestGenerator{
		deploymentTmpl:   deploymentTmpl,
		serviceTmpl:      serviceTmpl,
		scaledObjectTmpl: scaledObjectTmpl,
	}, nil
}

// GenerateDeployment generates a Deployment YAML for the group.
func (g *ManifestGenerator) GenerateDeployment(cfg ManifestConfig) ([]byte, error) {
	cfg = applyDefaults(cfg)
	var buf bytes.Buffer
	if err := g.deploymentTmpl.Execute(&buf, cfg); err != nil {
		return nil, fmt.Errorf("execute deployment template: %w", err)
	}
	return buf.Bytes(), nil
}

// GenerateService generates a Service YAML for the group.
func (g *ManifestGenerator) GenerateService(cfg ManifestConfig) ([]byte, error) {
	cfg = applyDefaults(cfg)
	var buf bytes.Buffer
	if err := g.serviceTmpl.Execute(&buf, cfg); err != nil {
		return nil, fmt.Errorf("execute service template: %w", err)
	}
	return buf.Bytes(), nil
}

// GenerateScaledObject generates a KEDA ScaledObject YAML for dynamic scaling mode.
func (g *ManifestGenerator) GenerateScaledObject(cfg ManifestConfig) ([]byte, error) {
	cfg = applyDefaults(cfg)
	var buf bytes.Buffer
	if err := g.scaledObjectTmpl.Execute(&buf, cfg); err != nil {
		return nil, fmt.Errorf("execute scaledobject template: %w", err)
	}
	return buf.Bytes(), nil
}

// GenerateAll generates all manifests for a group and writes them to outputDir.
// For dynamic scaling mode, it also generates the KEDA ScaledObject.
// The dynamicMode parameter determines if the ScaledObject should be generated.
func (g *ManifestGenerator) GenerateAll(cfg ManifestConfig, outputDir string, dynamicMode bool) error {
	// Create output directory.
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}

	// Generate Deployment.
	deployment, err := g.GenerateDeployment(cfg)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(outputDir, "deployment.yaml"), deployment, 0644); err != nil {
		return fmt.Errorf("write deployment.yaml: %w", err)
	}

	// Generate Service.
	service, err := g.GenerateService(cfg)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(outputDir, "service.yaml"), service, 0644); err != nil {
		return fmt.Errorf("write service.yaml: %w", err)
	}

	// Generate KEDA ScaledObject only for dynamic scaling mode.
	if dynamicMode {
		scaledObject, err := g.GenerateScaledObject(cfg)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(outputDir, "keda-scaledobject.yaml"), scaledObject, 0644); err != nil {
			return fmt.Errorf("write keda-scaledobject.yaml: %w", err)
		}
	}

	return nil
}

// applyDefaults fills in default values for optional ManifestConfig fields.
func applyDefaults(cfg ManifestConfig) ManifestConfig {
	if cfg.Namespace == "" {
		cfg.Namespace = "flow-workers"
	}
	if cfg.ServiceAccount == "" {
		cfg.ServiceAccount = "flow-worker"
	}
	if cfg.CPURequest == "" {
		cfg.CPURequest = "100m"
	}
	if cfg.CPULimit == "" {
		cfg.CPULimit = "500m"
	}
	if cfg.MemoryRequest == "" {
		cfg.MemoryRequest = "128Mi"
	}
	if cfg.MemoryLimit == "" {
		cfg.MemoryLimit = "512Mi"
	}
	if cfg.PrometheusAddr == "" {
		cfg.PrometheusAddr = "http://prometheus:9090"
	}
	if cfg.ScaleDownDelay == 0 {
		cfg.ScaleDownDelay = 300 // 5 minutes default
	}
	if cfg.MaxReplicas == 0 {
		cfg.MaxReplicas = 10
	}
	return cfg
}
