package flow

import (
	"context"
	"encoding/json"
)

// Message represents an incoming message from a consumer. It carries the raw
// payload plus metadata needed for at-least-once processing.
type Message struct {
	// Key is the message/record key (may be empty).
	Key []byte
	// Value is the message payload.
	Value []byte
	// Headers are optional key-value metadata attached to the message.
	Headers map[string]string
	// Metadata carries consumer-specific fields (topic, partition, offset, etc.).
	Metadata map[string]any
}

// messageTriggerHandler is the entry point for message-driven flows. It maps the
// incoming Message to Ctx.Input and walks the child body. It is similar to
// triggerHandler but driven by messages, not HTTP requests.
type messageTriggerHandler struct{}

// Exec implements NodeHandler. It validates the spec, maps the Message fields
// from context into Ctx.Input, and walks the children.
func (messageTriggerHandler) Exec(ctx context.Context, c *Ctx, n Node, dep Deps, w Walker) (Directive, error) {
	spec, err := parseSpec[MessageTriggerSpec](n.Spec)
	if err != nil {
		return Directive{}, err
	}
	if spec.ConnectionKey == "" {
		return Directive{}, validationf("messageTrigger %q missing connectionKey", n.ID)
	}
	if spec.Topic == "" {
		return Directive{}, validationf("messageTrigger %q missing topic", n.ID)
	}
	if spec.FlowID == "" {
		return Directive{}, validationf("messageTrigger %q missing flowId", n.ID)
	}
	if len(n.Children) == 0 {
		return Directive{}, validationf("messageTrigger %q must have at least one child", n.ID)
	}

	// The message should already be mapped into Ctx.Input by the MessageDispatcher
	// before calling Run. We verify it's present to catch wiring bugs early.
	if c.Input == nil || len(c.Input) == 0 {
		return Directive{}, validationf("messageTrigger %q has no input (message not mapped)", n.ID)
	}

	return walkChildren(ctx, n.Children, c, dep, w)
}

// messageKey is the context key for passing the Message to the handler.
type messageKey struct{}

// WithMessage attaches a Message to a context for retrieval by handlers.
func WithMessage(ctx context.Context, msg *Message) context.Context {
	return context.WithValue(ctx, messageKey{}, msg)
}

// MessageFrom retrieves the Message from a context, if present.
func MessageFrom(ctx context.Context) (*Message, bool) {
	msg, ok := ctx.Value(messageKey{}).(*Message)
	return msg, ok
}

// MapMessageToInput converts a Message into the Ctx.Input format expected by
// message-triggered flows. The layout is:
//
//	{
//	  "key": <string or nil>,
//	  "value": <parsed JSON or raw string>,
//	  "headers": <map[string]string>,
//	  "metadata": <map[string]any>
//	}
func MapMessageToInput(msg *Message) map[string]any {
	input := make(map[string]any, 4)

	// Key: decode as string if non-empty
	if len(msg.Key) > 0 {
		input["key"] = string(msg.Key)
	}

	// Value: try to decode as JSON, fall back to string
	if len(msg.Value) > 0 {
		var parsed any
		if err := json.Unmarshal(msg.Value, &parsed); err == nil {
			input["value"] = parsed
		} else {
			input["value"] = string(msg.Value)
		}
	}

	// Headers: copy as-is
	if len(msg.Headers) > 0 {
		headers := make(map[string]any, len(msg.Headers))
		for k, v := range msg.Headers {
			headers[k] = v
		}
		input["headers"] = headers
	} else {
		input["headers"] = map[string]any{}
	}

	// Metadata: copy as-is
	if len(msg.Metadata) > 0 {
		input["metadata"] = msg.Metadata
	} else {
		input["metadata"] = map[string]any{}
	}

	return input
}
