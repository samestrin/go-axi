package goaxi

import "testing"

// The exit codes ship as constants rather than as a rule in a style guide,
// because a documented convention drifts and a compiled constant cannot. The
// values are cadence-axi's, which is a superset of atcr's 0/1/2.
//
// The 4-vs-1 distinction is the load-bearing one: a checker correctly reporting
// "this did not pass" is a successful run with a real result, not a broken tool.
// Collapsing them makes "the tool crashed" indistinguishable from "your input is
// non-conforming", and an agent cannot decide whether to retry.
func TestExitCodes_HaveTheAgreedValues(t *testing.T) {
	cases := []struct {
		name string
		got  ExitCode
		want int
	}{
		{"ExitOK", ExitOK, 0},
		{"ExitError", ExitError, 1},
		{"ExitUsage", ExitUsage, 2},
		{"ExitNotFound", ExitNotFound, 3},
		{"ExitValidation", ExitValidation, 4},
	}
	for _, c := range cases {
		if int(c.got) != c.want {
			t.Errorf("%s = %d, want %d; these values are a cross-project contract", c.name, int(c.got), c.want)
		}
	}
}

func TestExitCode_String(t *testing.T) {
	for _, c := range []ExitCode{ExitOK, ExitError, ExitUsage, ExitNotFound, ExitValidation} {
		if s := c.String(); s == "" {
			t.Errorf("ExitCode(%d) must have a readable name for error messages", int(c))
		}
	}
	// An out-of-range code must still render rather than return "".
	if s := ExitCode(99).String(); s == "" {
		t.Error("an unknown exit code must still render something")
	}
}

// Validation and Error must never collapse into the same value, which is the
// specific mistake this set exists to prevent.
func TestExitCodes_ValidationIsDistinctFromError(t *testing.T) {
	if ExitValidation == ExitError {
		t.Fatal("ExitValidation must differ from ExitError: 'it did not pass' is not 'the tool broke'")
	}
	if ExitNotFound == ExitError {
		t.Fatal("ExitNotFound must differ from ExitError")
	}
}
