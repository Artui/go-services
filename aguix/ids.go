package aguix

import (
	"fmt"
	"sync/atomic"
)

// sequentialIDs returns a generator of ids unique within one process.
//
// Deliberately not random. A run's transcript is something a test asserts and a
// person reads while debugging, and ids that change every run make a recorded
// stream impossible to compare with the next one. The protocol asks only that
// an id be unique within its run, and a counter satisfies that.
//
// The counter is per generator, so two toolboxes do not interleave, and atomic,
// so one toolbox serving concurrent runs does not hand out a duplicate.
func sequentialIDs(prefix string) func() string {
	var counter atomic.Int64
	return func() string { return fmt.Sprintf("%s-%d", prefix, counter.Add(1)) }
}
