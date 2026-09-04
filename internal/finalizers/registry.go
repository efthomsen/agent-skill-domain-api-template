// Package finalizers decouples the leased AI-execution loop from the
// specific business logic each task_key applies on completion. A resource
// package registers a Finalizer for the task_keys it owns; the executions
// package only knows how to look one up by name.
package finalizers

import (
	"fmt"
	"sort"

	"github.com/pocketbase/pocketbase/core"
)

// Finalizer applies a completed execution's output to its target record,
// returning the result to store on the execution.
type Finalizer func(tx core.App, execution *core.Record, output map[string]any) (map[string]any, error)

// Registry maps task_key to the Finalizer that knows how to complete it.
type Registry struct {
	entries map[string]Finalizer
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{entries: map[string]Finalizer{}}
}

// Register adds f for taskKey. Registering the same taskKey twice is a
// programming error, caught here rather than silently overwriting.
func (r *Registry) Register(taskKey string, f Finalizer) error {
	if _, exists := r.entries[taskKey]; exists {
		return fmt.Errorf("finalizer already registered for task key %q", taskKey)
	}
	r.entries[taskKey] = f
	return nil
}

// Lookup returns the Finalizer registered for taskKey, if any. Safe to
// call on a nil *Registry (always reports not found).
func (r *Registry) Lookup(taskKey string) (Finalizer, bool) {
	if r == nil {
		return nil, false
	}
	f, ok := r.entries[taskKey]
	return f, ok
}

// ValidateCoverage fails loudly if any enabled row in ai_task_definitions
// has no registered finalizer — meant to be called at boot, before the
// server starts accepting requests, so a live data/code mismatch is a
// startup failure, not a silent runtime gap discovered later.
func ValidateCoverage(app core.App, registry *Registry) error {
	definitions, err := app.FindRecordsByFilter("ai_task_definitions", "enabled = true", "key", 0, 0, nil)
	if err != nil {
		return err
	}

	var missing []string
	for _, definition := range definitions {
		key := definition.GetString("key")
		if _, ok := registry.Lookup(key); !ok {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("no registered finalizer for active task key(s): %v", missing)
	}
	return nil
}
