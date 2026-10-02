package decision

import (
	"context"

	"nzr-rules-engine/internal/observ"
)

// backend is the zen-go binding abstracted behind the cgo/!cgo split. The cgo
// build (zen_cgo.go) returns a real zen.Engine-backed backend; the !cgo build
// (zen_stub.go) returns one whose compile fails with a Validation-class error so
// a non-CGO build COMPILES but never silently no-ops. The Engine never imports
// zen-go directly — only through this seam.
type backend interface {
	// compile parses JDM bytes into a Compiled handle (compile-once step).
	compile(jdm []byte) (Compiled, error)
	// close frees engine-level resources (the Rust-side engine) on shutdown.
	close()
}

// envFunc extracts the request environment from ctx (per-env isolation). The
// default reads an unexported context key; cmd/engine/httpapi stamps it.
type envFunc func(ctx context.Context) string

// Engine implements decision.Evaluator over a (id,version) compiled cache and a
// JDMLoader port. It is the concrete type cmd/engine wires; Close frees the
// cached graphs and the engine-level backend on graceful shutdown.
type Engine struct {
	loader  JDMLoader
	cache   *compiledCache
	backend backend
	log     observ.Logger
	trace   observ.Tracer
	envOf   envFunc
}

// compile-time assertion that *Engine satisfies the frozen Evaluator seam.
var _ Evaluator = (*Engine)(nil)

// Option configures an Engine at construction.
type Option func(*Engine)

// WithLogger sets the structured logger (never logs input values at info).
func WithLogger(l observ.Logger) Option { return func(e *Engine) { e.log = l } }

// WithTracer sets the span tracer.
func WithTracer(t observ.Tracer) Option { return func(e *Engine) { e.trace = t } }

// WithEnvFunc overrides how the request environment is read from ctx.
func WithEnvFunc(f envFunc) Option { return func(e *Engine) { e.envOf = f } }

// newEngine assembles an Engine from a loader and a backend. The cgo/!cgo
// constructors (New in zen_cgo.go / zen_stub.go) call this with their backend.
func newEngine(loader JDMLoader, be backend, opts ...Option) *Engine {
	e := &Engine{
		loader:  loader,
		backend: be,
		envOf:   EnvFromContext,
	}
	e.cache = newCompiledCache(be.compile)
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// Evaluate resolves the JDM for jdmID in the request's env, compiles it once per
// (id,version), and evaluates input against the compiled graph. ctx is honored
// around the evaluation (zen-go cannot cancel mid-eval). Errors are classified:
// a loader miss => NotFound, a malformed JDM / bad input => Validation, a ctx
// deadline => Timeout.
func (e *Engine) Evaluate(ctx context.Context, jdmID string, input map[string]any) (map[string]any, error) {
	env := ""
	if e.envOf != nil {
		env = e.envOf(ctx)
	}

	jdm, version, err := e.loader.LoadJDM(ctx, env, jdmID)
	if err != nil {
		return nil, wrapErr(NotFound, jdmID, 0, "load jdm", err)
	}

	compiled, err := e.cache.getOrCompile(jdmID, version, jdm)
	if err != nil {
		return nil, classifyEvalErr(jdmID, version, err)
	}

	out, err := compiled.Eval(ctx, input)
	if err != nil {
		return nil, classifyEvalErr(jdmID, version, err)
	}
	return out, nil
}

// Close frees the compiled-graph cache and the engine-level backend. cmd/engine
// calls it on graceful shutdown so the Rust-side graphs are released.
func (e *Engine) Close() error {
	e.cache.Close()
	e.backend.close()
	return nil
}
