package flow

import (
	"context"
	"fmt"
	"sync"
)

// FlowLookup resolves a flow tree by connection key and topic. Implementations
// typically read from a config store or in-memory cache.
type FlowLookup interface {
	// GetMessageFlow returns the flow tree and version for the given connection+topic.
	// Returns (nil, 0, ErrNotFound) if no flow is registered for this route.
	GetMessageFlow(ctx context.Context, connectionKey, topic string) (*Node, Version, error)
}

// MessageDispatcher bridges ConsumerManager and Interpreter. Given a Message, it
// looks up the registered flow by connectionKey+topic, builds a Ctx, and calls
// interpreter.Run(). On flow error with OnError != 'continue', it returns an error
// to trigger dead-letter or retry.
type MessageDispatcher struct {
	mu      sync.RWMutex
	interp  *Interpreter
	lookup  FlowLookup
	deps    Deps
	env     string
	onError OnError // default error handling: "fail" (default) or "continue"
}

// MessageDispatcherConfig configures the MessageDispatcher.
type MessageDispatcherConfig struct {
	// Lookup resolves flows by connection+topic.
	Lookup FlowLookup
	// Deps provides the connection registry, decision evaluator, etc.
	Deps Deps
	// Env is the environment for the flows (e.g., "production", "staging").
	Env string
	// OnError is the default error handling mode ("fail" or "continue").
	// Defaults to "fail" if empty.
	OnError OnError
}

// NewMessageDispatcher creates a MessageDispatcher with the given configuration.
func NewMessageDispatcher(interp *Interpreter, cfg MessageDispatcherConfig) *MessageDispatcher {
	onError := cfg.OnError
	if onError == "" {
		onError = OnErrorFail
	}
	return &MessageDispatcher{
		interp:  interp,
		lookup:  cfg.Lookup,
		deps:    cfg.Deps,
		env:     cfg.Env,
		onError: onError,
	}
}

// Dispatch processes a message by looking up the flow and running it. The flow is
// identified by the connectionKey+topic combination. Returns an error if:
//   - No flow is registered for the route (ErrNotFound)
//   - The flow execution fails and onError is "fail"
//
// At-least-once semantics: the caller (ConsumerManager) should only ack the message
// after Dispatch returns nil. On error, the consumer implementation handles retry
// or dead-letter based on its configuration.
func (d *MessageDispatcher) Dispatch(ctx context.Context, connectionKey, topic string, msg *Message) error {
	if msg == nil {
		return validationf("nil message")
	}

	// Look up the flow for this connection+topic
	tree, ver, err := d.lookup.GetMessageFlow(ctx, connectionKey, topic)
	if err != nil {
		return fmt.Errorf("lookup flow for %s/%s: %w", connectionKey, topic, err)
	}
	if tree == nil {
		return newErr(ClassNotFound, fmt.Sprintf("no flow for %s/%s", connectionKey, topic))
	}

	// Build the Ctx with the message mapped to Input
	input := MapMessageToInput(msg)
	c := NewCtx("", "", d.env, input)

	// Attach the message to the context for downstream handlers if needed
	ctx = WithMessage(ctx, msg)

	// Run the flow
	d.mu.RLock()
	deps := d.deps
	onError := d.onError
	d.mu.RUnlock()

	err = d.interp.Run(ctx, tree, ver, c, deps)
	if err != nil {
		if onError == OnErrorContinue {
			// Log but don't fail — the message is considered processed
			if deps.Log != nil {
				deps.Log.Emit(ctx, "warn", "message_dispatch.continue_on_error", map[string]any{
					"connection_key": connectionKey,
					"topic":          topic,
					"err":            err.Error(),
				})
			}
			return nil
		}
		return fmt.Errorf("flow execution failed: %w", err)
	}

	return nil
}

// UpdateDeps replaces the dependencies (connection registry, decision evaluator,
// etc.) used by the dispatcher. This is called during hot-reload when the worker
// reloads its configuration.
func (d *MessageDispatcher) UpdateDeps(deps Deps) {
	d.mu.Lock()
	d.deps = deps
	d.mu.Unlock()
}

// routeKey builds the lookup key for a connection+topic combination.
func routeKey(connectionKey, topic string) string {
	return connectionKey + "/" + topic
}

// InMemoryFlowLookup is a simple in-memory implementation of FlowLookup for
// testing and simple deployments. Production systems typically use a config
// store-backed implementation.
type InMemoryFlowLookup struct {
	mu    sync.RWMutex
	flows map[string]flowEntry
}

type flowEntry struct {
	tree *Node
	ver  Version
}

// NewInMemoryFlowLookup creates an empty in-memory flow lookup.
func NewInMemoryFlowLookup() *InMemoryFlowLookup {
	return &InMemoryFlowLookup{
		flows: make(map[string]flowEntry),
	}
}

// Register adds or updates a flow for the given connection+topic.
func (l *InMemoryFlowLookup) Register(connectionKey, topic string, tree *Node, ver Version) {
	l.mu.Lock()
	l.flows[routeKey(connectionKey, topic)] = flowEntry{tree: tree, ver: ver}
	l.mu.Unlock()
}

// Unregister removes the flow for the given connection+topic.
func (l *InMemoryFlowLookup) Unregister(connectionKey, topic string) {
	l.mu.Lock()
	delete(l.flows, routeKey(connectionKey, topic))
	l.mu.Unlock()
}

// GetMessageFlow implements FlowLookup.
func (l *InMemoryFlowLookup) GetMessageFlow(ctx context.Context, connectionKey, topic string) (*Node, Version, error) {
	l.mu.RLock()
	entry, ok := l.flows[routeKey(connectionKey, topic)]
	l.mu.RUnlock()

	if !ok {
		return nil, Version{}, newErr(ClassNotFound, fmt.Sprintf("no flow for %s/%s", connectionKey, topic))
	}
	return entry.tree, entry.ver, nil
}
