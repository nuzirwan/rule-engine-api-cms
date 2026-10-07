package gateway

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestNewManifestGenerator(t *testing.T) {
	gen, err := NewManifestGenerator()
	if err != nil {
		t.Fatalf("NewManifestGenerator() error: %v", err)
	}
	if gen == nil {
		t.Fatal("expected non-nil ManifestGenerator")
	}
}

func TestManifestGenerator_GenerateDeployment(t *testing.T) {
	gen, err := NewManifestGenerator()
	if err != nil {
		t.Fatalf("NewManifestGenerator() error: %v", err)
	}

	cfg := ManifestConfig{
		Group:          "orders",
		Namespace:      "flow-workers",
		WorkerImage:    "myregistry/worker:v1.0.0",
		ServiceAccount: "flow-worker-sa",
		MinReplicas:    2,
		MaxReplicas:    10,
		CPURequest:     "200m",
		CPULimit:       "1000m",
		MemoryRequest:  "256Mi",
		MemoryLimit:    "1Gi",
	}

	yamlBytes, err := gen.GenerateDeployment(cfg)
	if err != nil {
		t.Fatalf("GenerateDeployment() error: %v", err)
	}

	// Verify YAML is valid.
	var parsed map[string]any
	if err := yaml.Unmarshal(yamlBytes, &parsed); err != nil {
		t.Fatalf("generated YAML is invalid: %v", err)
	}

	// Verify key fields.
	if parsed["apiVersion"] != "apps/v1" {
		t.Errorf("expected apiVersion=apps/v1, got %v", parsed["apiVersion"])
	}
	if parsed["kind"] != "Deployment" {
		t.Errorf("expected kind=Deployment, got %v", parsed["kind"])
	}

	metadata, ok := parsed["metadata"].(map[string]any)
	if !ok {
		t.Fatal("expected metadata to be a map")
	}
	if metadata["name"] != "worker-orders" {
		t.Errorf("expected name=worker-orders, got %v", metadata["name"])
	}
	if metadata["namespace"] != "flow-workers" {
		t.Errorf("expected namespace=flow-workers, got %v", metadata["namespace"])
	}

	// Verify replicas.
	spec, ok := parsed["spec"].(map[string]any)
	if !ok {
		t.Fatal("expected spec to be a map")
	}
	if spec["replicas"] != 2 {
		t.Errorf("expected replicas=2, got %v", spec["replicas"])
	}

	// Verify container image.
	yamlStr := string(yamlBytes)
	if !strings.Contains(yamlStr, "image: myregistry/worker:v1.0.0") {
		t.Errorf("expected image in YAML, got:\n%s", yamlStr)
	}
	if !strings.Contains(yamlStr, "--group=orders") {
		t.Errorf("expected --group=orders in args")
	}
}

func TestManifestGenerator_GenerateDeployment_Defaults(t *testing.T) {
	gen, err := NewManifestGenerator()
	if err != nil {
		t.Fatalf("NewManifestGenerator() error: %v", err)
	}

	// Minimal config — should apply defaults.
	cfg := ManifestConfig{
		Group: "minimal",
	}

	yamlBytes, err := gen.GenerateDeployment(cfg)
	if err != nil {
		t.Fatalf("GenerateDeployment() error: %v", err)
	}

	yamlStr := string(yamlBytes)

	// Verify defaults applied.
	if !strings.Contains(yamlStr, "namespace: flow-workers") {
		t.Error("expected default namespace")
	}
	if !strings.Contains(yamlStr, "serviceAccountName: flow-worker") {
		t.Error("expected default serviceAccountName")
	}
	if !strings.Contains(yamlStr, "cpu: 100m") {
		t.Error("expected default CPU request")
	}
	if !strings.Contains(yamlStr, "memory: 128Mi") {
		t.Error("expected default memory request")
	}
}

func TestManifestGenerator_GenerateService(t *testing.T) {
	gen, err := NewManifestGenerator()
	if err != nil {
		t.Fatalf("NewManifestGenerator() error: %v", err)
	}

	cfg := ManifestConfig{
		Group:     "payments",
		Namespace: "custom-ns",
	}

	yamlBytes, err := gen.GenerateService(cfg)
	if err != nil {
		t.Fatalf("GenerateService() error: %v", err)
	}

	// Verify YAML is valid.
	var parsed map[string]any
	if err := yaml.Unmarshal(yamlBytes, &parsed); err != nil {
		t.Fatalf("generated YAML is invalid: %v", err)
	}

	if parsed["apiVersion"] != "v1" {
		t.Errorf("expected apiVersion=v1, got %v", parsed["apiVersion"])
	}
	if parsed["kind"] != "Service" {
		t.Errorf("expected kind=Service, got %v", parsed["kind"])
	}

	metadata, ok := parsed["metadata"].(map[string]any)
	if !ok {
		t.Fatal("expected metadata to be a map")
	}
	if metadata["name"] != "worker-payments" {
		t.Errorf("expected name=worker-payments, got %v", metadata["name"])
	}
	if metadata["namespace"] != "custom-ns" {
		t.Errorf("expected namespace=custom-ns, got %v", metadata["namespace"])
	}
}

func TestManifestGenerator_GenerateScaledObject(t *testing.T) {
	gen, err := NewManifestGenerator()
	if err != nil {
		t.Fatalf("NewManifestGenerator() error: %v", err)
	}

	cfg := ManifestConfig{
		Group:          "webhooks",
		Namespace:      "flow-workers",
		MinReplicas:    0,
		MaxReplicas:    5,
		ScaleDownDelay: 600, // 10 minutes
		PrometheusAddr: "http://prometheus.monitoring:9090",
	}

	yamlBytes, err := gen.GenerateScaledObject(cfg)
	if err != nil {
		t.Fatalf("GenerateScaledObject() error: %v", err)
	}

	// Verify YAML is valid.
	var parsed map[string]any
	if err := yaml.Unmarshal(yamlBytes, &parsed); err != nil {
		t.Fatalf("generated YAML is invalid: %v", err)
	}

	if parsed["apiVersion"] != "keda.sh/v1alpha1" {
		t.Errorf("expected apiVersion=keda.sh/v1alpha1, got %v", parsed["apiVersion"])
	}
	if parsed["kind"] != "ScaledObject" {
		t.Errorf("expected kind=ScaledObject, got %v", parsed["kind"])
	}

	metadata, ok := parsed["metadata"].(map[string]any)
	if !ok {
		t.Fatal("expected metadata to be a map")
	}
	if metadata["name"] != "worker-webhooks-scaler" {
		t.Errorf("expected name=worker-webhooks-scaler, got %v", metadata["name"])
	}

	spec, ok := parsed["spec"].(map[string]any)
	if !ok {
		t.Fatal("expected spec to be a map")
	}
	if spec["minReplicaCount"] != 0 {
		t.Errorf("expected minReplicaCount=0, got %v", spec["minReplicaCount"])
	}
	if spec["maxReplicaCount"] != 5 {
		t.Errorf("expected maxReplicaCount=5, got %v", spec["maxReplicaCount"])
	}
	if spec["cooldownPeriod"] != 600 {
		t.Errorf("expected cooldownPeriod=600, got %v", spec["cooldownPeriod"])
	}

	// Verify Prometheus trigger.
	yamlStr := string(yamlBytes)
	if !strings.Contains(yamlStr, "serverAddress: http://prometheus.monitoring:9090") {
		t.Error("expected Prometheus server address")
	}
	if !strings.Contains(yamlStr, `group="webhooks"`) {
		t.Error("expected group label in Prometheus query")
	}
}

func TestManifestGenerator_GenerateAll(t *testing.T) {
	gen, err := NewManifestGenerator()
	if err != nil {
		t.Fatalf("NewManifestGenerator() error: %v", err)
	}

	tmpDir := t.TempDir()
	outputDir := filepath.Join(tmpDir, "orders")

	cfg := ManifestConfig{
		Group:       "orders",
		Namespace:   "flow-workers",
		WorkerImage: "worker:latest",
	}

	// Generate without KEDA ScaledObject (static mode).
	if err := gen.GenerateAll(cfg, outputDir, false); err != nil {
		t.Fatalf("GenerateAll() error: %v", err)
	}

	// Verify files created.
	deploymentPath := filepath.Join(outputDir, "deployment.yaml")
	if _, err := os.Stat(deploymentPath); os.IsNotExist(err) {
		t.Error("expected deployment.yaml to be created")
	}

	servicePath := filepath.Join(outputDir, "service.yaml")
	if _, err := os.Stat(servicePath); os.IsNotExist(err) {
		t.Error("expected service.yaml to be created")
	}

	scaledObjectPath := filepath.Join(outputDir, "keda-scaledobject.yaml")
	if _, err := os.Stat(scaledObjectPath); !os.IsNotExist(err) {
		t.Error("expected keda-scaledobject.yaml NOT to be created for static mode")
	}
}

func TestManifestGenerator_GenerateAll_DynamicMode(t *testing.T) {
	gen, err := NewManifestGenerator()
	if err != nil {
		t.Fatalf("NewManifestGenerator() error: %v", err)
	}

	tmpDir := t.TempDir()
	outputDir := filepath.Join(tmpDir, "dynamic-group")

	cfg := ManifestConfig{
		Group:       "dynamic-group",
		Namespace:   "flow-workers",
		WorkerImage: "worker:latest",
	}

	// Generate with KEDA ScaledObject (dynamic mode).
	if err := gen.GenerateAll(cfg, outputDir, true); err != nil {
		t.Fatalf("GenerateAll() error: %v", err)
	}

	// Verify KEDA ScaledObject created.
	scaledObjectPath := filepath.Join(outputDir, "keda-scaledobject.yaml")
	if _, err := os.Stat(scaledObjectPath); os.IsNotExist(err) {
		t.Error("expected keda-scaledobject.yaml to be created for dynamic mode")
	}
}

func TestManifestGenerator_GenerateAll_Idempotent(t *testing.T) {
	gen, err := NewManifestGenerator()
	if err != nil {
		t.Fatalf("NewManifestGenerator() error: %v", err)
	}

	tmpDir := t.TempDir()
	outputDir := filepath.Join(tmpDir, "idempotent")

	cfg := ManifestConfig{
		Group:       "idempotent",
		WorkerImage: "worker:v1",
	}

	// Generate twice — should not error.
	if err := gen.GenerateAll(cfg, outputDir, true); err != nil {
		t.Fatalf("first GenerateAll() error: %v", err)
	}

	// Modify config and regenerate.
	cfg.WorkerImage = "worker:v2"
	if err := gen.GenerateAll(cfg, outputDir, true); err != nil {
		t.Fatalf("second GenerateAll() error: %v", err)
	}

	// Verify updated content.
	deploymentBytes, err := os.ReadFile(filepath.Join(outputDir, "deployment.yaml"))
	if err != nil {
		t.Fatalf("read deployment.yaml: %v", err)
	}
	if !strings.Contains(string(deploymentBytes), "worker:v2") {
		t.Error("expected updated worker image in deployment")
	}
}
