// Package celx evaluates DRA device selectors with the upstream CEL package,
// following the allocator's rules, and reports which selector rejected a
// device.
package celx

import (
	"context"
	"fmt"

	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/dynamic-resource-allocation/cel"
)

// maxCachedExpressions bounds the compiled expression cache. A cluster has a
// handful of classes and claim selectors, so this is generous.
const maxCachedExpressions = 256

// Evaluator compiles and evaluates selector expressions, caching compiled
// programs. It is safe for concurrent use.
type Evaluator struct {
	cache *cel.Cache
}

// NewEvaluator returns an Evaluator. Optional CEL features are enabled so
// that devices using list attributes or consumable capacity are evaluated
// instead of failing. The scheduler may have them off; matching that is part
// of feature inference, which belongs to "why".
func NewEvaluator() *Evaluator {
	return &Evaluator{cache: cel.NewCache(maxCachedExpressions, cel.Features{
		EnableConsumableCapacity: true,
		EnableListTypeAttributes: true,
	})}
}

// Mismatch identifies the first selector that evaluated to false.
type Mismatch struct {
	Index      int
	Expression string
}

// SelectorError is a compile or runtime error in one selector. The
// allocator treats both as fatal for the whole claim, not as a non-match.
type SelectorError struct {
	Index      int
	Expression string
	Compile    bool
	Err        error
}

func (e *SelectorError) Error() string {
	kind := "runtime"
	if e.Compile {
		kind = "compile"
	}
	return fmt.Sprintf("selector #%d: CEL %s error: %v", e.Index, kind, e.Err)
}

func (e *SelectorError) Unwrap() error { return e.Err }

// Match evaluates selectors in order against a device published by driver,
// stopping at the first one that does not match, as the allocator does. It
// returns nil, nil when all of them match.
func (e *Evaluator) Match(ctx context.Context, driver string, device *resourceapi.Device, selectors []resourceapi.DeviceSelector) (*Mismatch, error) {
	// Same inputs as the stable allocator's selectorsMatch.
	input := cel.Device{Driver: driver, Attributes: device.Attributes, Capacity: device.Capacity}
	for i, selector := range selectors {
		if selector.CEL == nil {
			return nil, &SelectorError{Index: i, Err: fmt.Errorf("selector has no cel expression; this kubectl-dra version does not know its type")}
		}
		expr := selector.CEL.Expression
		compiled := e.cache.GetOrCompile(expr)
		if compiled.Error != nil {
			return nil, &SelectorError{Index: i, Expression: expr, Compile: true, Err: compiled.Error}
		}
		matches, _, err := compiled.DeviceMatches(ctx, input)
		if err != nil {
			return nil, &SelectorError{Index: i, Expression: expr, Err: cel.EnhanceRuntimeError(err)}
		}
		if !matches {
			return &Mismatch{Index: i, Expression: expr}, nil
		}
	}
	return nil, nil
}
