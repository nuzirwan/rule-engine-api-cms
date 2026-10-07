package gateway

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"nzr-rules-engine/internal/config"
	"nzr-rules-engine/internal/observ"
)

// ErrEphemeralNotSupported is returned when ephemeral scaling mode is requested.
var ErrEphemeralNotSupported = fmt.Errorf("ephemeral scaling mode not yet supported")

// GroupReader is the interface for reading group configuration from the store.
type GroupReader interface {
	GetGroup(ctx context.Context, env, groupID string) (config.Group, error)
}

// ScalerConfig holds Scaler construction options.
type ScalerConfig struct {
	K8sClient      kubernetes.Interface
	Namespace      string
	Store          GroupReader
	Log            observ.Logger
	Metrics        *GatewayMetrics
	StartupTimeout time.Duration
}

// Scaler manages K8s deployments for worker groups. It reads group config from
// the store and scales deployments according to the scaling mode. Scaler only
// reads/updates existing deployments — it never creates them (manifest generation
// is separate via CLI).
type Scaler struct {
	k8s            kubernetes.Interface
	namespace      string
	store          GroupReader
	log            observ.Logger
	metrics        *GatewayMetrics
	startupTimeout time.Duration
}

// NewScaler creates a Scaler with the given configuration.
func NewScaler(cfg ScalerConfig) *Scaler {
	timeout := cfg.StartupTimeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	return &Scaler{
		k8s:            cfg.K8sClient,
		namespace:      cfg.Namespace,
		store:          cfg.Store,
		log:            cfg.Log,
		metrics:        cfg.Metrics,
		startupTimeout: timeout,
	}
}

// EnsureReady ensures the worker for the group is ready to receive requests.
// Behavior depends on scaling mode:
//   - static: verify deployment has >= minReplicas ready
//   - dynamic: scale to at least 1 replica if currently at 0, wait for ready
//   - ephemeral: not supported yet (returns error)
func (s *Scaler) EnsureReady(ctx context.Context, group string) error {
	cfg, err := s.store.GetGroup(ctx, "", group)
	if err != nil {
		return fmt.Errorf("get group config: %w", err)
	}

	switch cfg.Scaling.Mode {
	case config.ScalingModeStatic:
		return s.ensureStaticDeployment(ctx, group, int32(cfg.Scaling.MinReplicas))

	case config.ScalingModeDynamic:
		return s.ensureDynamicDeployment(ctx, group)

	case config.ScalingModeEphemeral:
		return ErrEphemeralNotSupported

	default:
		return fmt.Errorf("unknown scaling mode: %s", cfg.Scaling.Mode)
	}
}

// ensureStaticDeployment verifies the deployment has >= minReplicas ready.
// For static mode, deployments always have minReplicas > 0.
func (s *Scaler) ensureStaticDeployment(ctx context.Context, group string, minReplicas int32) error {
	s.logEvent(ctx, "debug", "scaler.ensure_static", group, map[string]any{
		"minReplicas": minReplicas,
	})
	s.incMetric(group, "ensure_ready")

	// Wait for ready pods >= minReplicas or timeout.
	return s.waitForReady(ctx, group, minReplicas)
}

// ensureDynamicDeployment scales up from 0 if needed and waits for at least 1 ready pod.
func (s *Scaler) ensureDynamicDeployment(ctx context.Context, group string) error {
	replicas, ready, err := s.GetDeploymentStatus(ctx, group)
	if err != nil {
		return err
	}

	s.logEvent(ctx, "debug", "scaler.ensure_dynamic", group, map[string]any{
		"replicas": replicas,
		"ready":    ready,
	})

	// If already has ready pods, nothing to do.
	if ready >= 1 {
		return nil
	}

	// Track cold start timing when scaling from zero.
	var coldStartTime time.Time
	isColdStart := replicas == 0

	// If replicas is 0, scale up to 1.
	if replicas == 0 {
		coldStartTime = time.Now()
		if err := s.ScaleUp(ctx, group, 1); err != nil {
			return err
		}
	}

	s.incMetric(group, "ensure_ready")

	// Wait for at least 1 ready pod.
	if err := s.waitForReady(ctx, group, 1); err != nil {
		return err
	}

	// Record cold start duration if this was a scale-from-zero.
	if isColdStart && s.metrics != nil {
		s.metrics.ObserveColdStart(group, time.Since(coldStartTime))
	}

	return nil
}

// ScaleUp sets the deployment replicas to at least the given minimum.
func (s *Scaler) ScaleUp(ctx context.Context, group string, minReplicas int32) error {
	deploymentName := "worker-" + group

	deployment, err := s.k8s.AppsV1().Deployments(s.namespace).Get(ctx, deploymentName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get deployment %s: %w", deploymentName, err)
	}

	currentReplicas := int32(0)
	if deployment.Spec.Replicas != nil {
		currentReplicas = *deployment.Spec.Replicas
	}

	// Only scale up if current < minimum.
	if currentReplicas >= minReplicas {
		return nil
	}

	s.logEvent(ctx, "info", "scaler.scaling_up", group, map[string]any{
		"fromReplicas": currentReplicas,
		"toReplicas":   minReplicas,
	})

	deployment.Spec.Replicas = &minReplicas
	_, err = s.k8s.AppsV1().Deployments(s.namespace).Update(ctx, deployment, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("update deployment %s replicas: %w", deploymentName, err)
	}

	s.incMetric(group, "scale_up")
	return nil
}

// ScaleDown allows the deployment to scale to 0 (removes any minimum override).
// For dynamic mode, this sets replicas to 0. For static mode, this is a no-op.
func (s *Scaler) ScaleDown(ctx context.Context, group string) error {
	cfg, err := s.store.GetGroup(ctx, "", group)
	if err != nil {
		return fmt.Errorf("get group config: %w", err)
	}

	// Static groups don't scale to 0.
	if cfg.Scaling.Mode == config.ScalingModeStatic {
		s.logEvent(ctx, "debug", "scaler.scale_down_noop", group, map[string]any{
			"reason": "static mode",
		})
		return nil
	}

	deploymentName := "worker-" + group

	deployment, err := s.k8s.AppsV1().Deployments(s.namespace).Get(ctx, deploymentName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get deployment %s: %w", deploymentName, err)
	}

	var zero int32 = 0
	deployment.Spec.Replicas = &zero

	s.logEvent(ctx, "info", "scaler.scaling_down", group, nil)

	_, err = s.k8s.AppsV1().Deployments(s.namespace).Update(ctx, deployment, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("update deployment %s replicas to 0: %w", deploymentName, err)
	}

	s.incMetric(group, "scale_down")
	return nil
}

// GetDeploymentStatus returns the current replica count and ready count for a group's deployment.
func (s *Scaler) GetDeploymentStatus(ctx context.Context, group string) (replicas int32, ready int32, err error) {
	deploymentName := "worker-" + group

	deployment, err := s.k8s.AppsV1().Deployments(s.namespace).Get(ctx, deploymentName, metav1.GetOptions{})
	if err != nil {
		return 0, 0, fmt.Errorf("get deployment %s: %w", deploymentName, err)
	}

	return deploymentReplicas(deployment), deployment.Status.ReadyReplicas, nil
}

// waitForReady polls the deployment status until at least minReady pods are ready or timeout.
func (s *Scaler) waitForReady(ctx context.Context, group string, minReady int32) error {
	deadline := time.Now().Add(s.startupTimeout)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if time.Now().After(deadline) {
				return fmt.Errorf("timeout waiting for worker %s to be ready (need %d ready)", group, minReady)
			}

			_, ready, err := s.GetDeploymentStatus(ctx, group)
			if err != nil {
				// Deployment might not exist yet, continue polling.
				s.logEvent(ctx, "debug", "scaler.wait_for_ready_poll", group, map[string]any{
					"error": err.Error(),
				})
				continue
			}

			if ready >= minReady {
				s.logEvent(ctx, "debug", "scaler.ready", group, map[string]any{
					"ready": ready,
				})
				if s.metrics != nil {
					s.metrics.SetWorkerReady(group, true)
					s.metrics.SetWorkerReplicas(group, int(ready))
				}
				return nil
			}
		}
	}
}

// logEvent emits a structured log event if a logger is configured.
func (s *Scaler) logEvent(ctx context.Context, level, event, group string, data map[string]any) {
	if s.log == nil {
		return
	}
	if data == nil {
		data = make(map[string]any)
	}
	data["group"] = group
	s.log.Emit(ctx, level, event, data)
}

// incMetric increments a scale operation metric if metrics are configured.
func (s *Scaler) incMetric(group, action string) {
	if s.metrics != nil {
		s.metrics.IncScaleOperation(group, action)
	}
}

// deploymentReplicas safely extracts the replicas from a deployment spec.
func deploymentReplicas(d *appsv1.Deployment) int32 {
	if d.Spec.Replicas == nil {
		return 1 // K8s default
	}
	return *d.Spec.Replicas
}
