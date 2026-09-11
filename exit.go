package goaxi

import "fmt"

// ExitCode is the process exit status an agent-facing CLI returns.
//
// These ship as constants rather than as a rule in a style guide because a
// documented convention drifts and a compiled constant cannot. The values are
// cadence-axi's set, which is a superset of atcr's 0/1/2.
type ExitCode int

const (
	// ExitOK is success.
	ExitOK ExitCode = 0

	// ExitError is a runtime failure inside the tool itself.
	ExitError ExitCode = 1

	// ExitUsage is a malformed invocation: an unknown subcommand or flag.
	//
	// AXI requires an unknown flag to fail loudly rather than be ignored. An
	// agent that invents a flag has to learn it did nothing, or it will trust
	// output that was never scoped the way it asked.
	ExitUsage ExitCode = 2

	// ExitNotFound is a path or item that does not exist.
	ExitNotFound ExitCode = 3

	// ExitValidation means the command ran correctly and the thing it checked
	// did not pass.
	//
	// This is deliberately distinct from ExitError, and it is the load-bearing
	// distinction in the set. A checker reporting "this does not conform" is a
	// successful run with a real result, not a broken tool. Collapsing the two
	// makes "the tool crashed" indistinguishable from "your input is wrong",
	// and an agent cannot tell whether retrying is worth anything.
	ExitValidation ExitCode = 4
)

// String renders the code for error messages and logs. A bare integer in a
// message tells an operator nothing.
func (c ExitCode) String() string {
	switch c {
	case ExitOK:
		return "ok"
	case ExitError:
		return "error"
	case ExitUsage:
		return "usage"
	case ExitNotFound:
		return "not-found"
	case ExitValidation:
		return "validation"
	default:
		return fmt.Sprintf("ExitCode(%d)", int(c))
	}
}
