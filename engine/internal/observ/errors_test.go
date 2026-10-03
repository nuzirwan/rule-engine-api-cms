package observ

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// fakeClassified reports a class via the Classified interface (as another
// slice's error wrapper would after adopting it).
type fakeClassified struct{ c string }

func (f fakeClassified) Error() string      { return "boom" }
func (f fakeClassified) ErrorClass() string { return f.c }

// TestClassOf covers the resolution order: Classified, context errors, default.
func TestClassOf(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want ErrorClass
	}{
		{"nil", nil, ClassInternal},
		{"classified upstream", Classify(ClassUpstream, "x", nil), ClassUpstream},
		{"classified validation", Validationf("bad %s", "token"), ClassValidation},
		{"wrapped classified", fmt.Errorf("ctx: %w", Classify(ClassNotFound, "x", nil)), ClassNotFound},
		{"deadline", context.DeadlineExceeded, ClassTimeout},
		{"canceled", context.Canceled, ClassTimeout},
		{"unknown", errors.New("nope"), ClassInternal},
		{"lowercase alias", fakeClassified{c: "timeout"}, ClassTimeout},
		{"underscore alias", fakeClassified{c: "not_found"}, ClassNotFound},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ClassOf(c.err); got != c.want {
				t.Fatalf("ClassOf(%v) = %s; want %s", c.err, got, c.want)
			}
		})
	}
}

// TestClassifyWrapsCause proves Classify preserves the cause for errors.Is/As.
func TestClassifyWrapsCause(t *testing.T) {
	sentinel := errors.New("root")
	err := Classify(ClassTimeout, "op failed", sentinel)
	if !errors.Is(err, sentinel) {
		t.Fatalf("Classify dropped the cause")
	}
	if ClassOf(err) != ClassTimeout {
		t.Fatalf("class not preserved")
	}
}
