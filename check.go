package goaxi

import (
	"encoding"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"time"

	toon "github.com/toon-format/toon-go"
)

// Tier describes the payload shape TOON encoding actually produces.
//
// The tiers are defined by measured behavior, not by what a format "should"
// handle. toon-go encodes ragged rows, list-valued fields and nested objects
// losslessly using a list form, so "can it be encoded" separates nothing useful.
// What differs is the shape emitted, and whether anything is lost.
type Tier int

const (
	// TierTabular is TOON's best case: uniform rows that encode as a tabular
	// array with a declared field list. This is where the token savings come
	// from.
	TierTabular Tier = iota

	// TierNested is lossless but not tabular. Ragged rows, list-valued fields
	// and nested objects land here. The payload is correct; it just does not
	// get the columnar win.
	TierNested

	// TierLossy means encoding silently discards data. Nothing in this tier
	// should be emitted as TOON.
	TierLossy
)

func (t Tier) String() string {
	switch t {
	case TierTabular:
		return "tabular"
	case TierNested:
		return "nested"
	case TierLossy:
		return "lossy"
	default:
		return fmt.Sprintf("Tier(%d)", int(t))
	}
}

// Verdict is the result of inspecting a value before encoding it.
//
// OK and Efficient are deliberately separate. Correctness and cost are
// orthogonal: a payload can be perfectly lossless and still cost more as TOON
// than as JSON, which is a reason to choose JSON for that command but never a
// reason to call the value broken.
type Verdict struct {
	// OK reports that encoding this value as TOON preserves its data.
	OK bool

	// Tier is the shape TOON encoding produces.
	Tier Tier

	// Efficient reports that the TOON payload is no larger than the JSON one.
	Efficient bool

	// Reason explains a false OK or Efficient in terms an operator can act on.
	Reason string
}

// textMarshalerType is the interface toon-go silently ignores.
var textMarshalerType = reflect.TypeOf((*encoding.TextMarshaler)(nil)).Elem()

// timeType is special-cased by toon-go and must NOT be treated as lossy even
// though time.Time implements TextMarshaler. Verified by probe: a time.Time
// encodes as `at: "2026-09-11T12:00:00Z"`, not as an empty value. Without this
// exclusion Check rejects every payload carrying a timestamp.
var timeType = reflect.TypeOf(time.Time{})

// maxWalkDepth bounds the lossy VALUE walk, which has no seen set of its own.
// The cycle guard does not use it: hasCycle terminates on identity, and a depth
// cap there would silently pass a cycle through to the encoder.
const maxWalkDepth = 100

// isLossyType reports whether toon-go drops values of this type.
//
// Only t's own method set is consulted. Checking reflect.PointerTo(t) as well
// would over-flag: a type whose pointer implements TextMarshaler but which is
// stored by value never has the method called, so nothing is lost.
func isLossyType(t reflect.Type) bool {
	if t == nil || t == timeType {
		return false
	}
	return t.Implements(textMarshalerType)
}

func lossyReason(t reflect.Type) string {
	return fmt.Sprintf("%s implements encoding.TextMarshaler, which toon-go ignores: "+
		"the value is silently dropped while its key is still emitted", typeName(t))
}

// tabularHeader matches a TOON tabular array header, e.g. `rows[2]{id,name}:`.
// The braced field list is the discriminator: a non-uniform array emits
// `rows[2]:` with no field list and falls back to indented list items.
var tabularHeader = regexp.MustCompile(`(?m)^\s*[^\[\]{}:]*\[\d+[^\]]*\]\{[^}]*\}:`)

// Check reports whether v can be encoded as TOON without losing data, what
// shape the payload takes, and whether TOON actually costs less than JSON.
//
// It exists because toon-go fails in ways neither an error check nor a length
// check catches, all confirmed against the pinned version:
//
//   - A defined string type (type Kind string) returns a real error. Loud.
//   - A TextMarshaler at top level returns a nil error with EMPTY output, so
//     the command prints nothing and exits zero.
//   - A TextMarshaler nested in a struct or map encodes to "m:" — key present,
//     value gone, nil error, non-zero length. A guard testing only err and
//     len(b) passes this while the data is lost.
//
// Loss is found by reflection rather than by round-tripping and comparing
// against the JSON projection. That comparison looks correct and is not:
// toon-go does not fall back to the json tag, so a field tagged only
// `json:"x"` encodes under its Go field name and the two projections disagree
// for reasons that have nothing to do with data loss.
func Check(v any) Verdict {
	// Sanitize is also the cycle and key-collision guard: it refuses a
	// self-referential value with *CycleError, so a cycle is caught here rather
	// than reaching toon.Marshal, where stack exhaustion would kill the process
	// unrecoverably.
	//
	// Check used to run its own hasCycle pass first. That became redundant when
	// Sanitize gained the guard in v0.1.1, and meant one call detected the same
	// cycle twice — duplication atcr's review flagged.
	clean, err := Sanitize(v)
	if err != nil {
		return Verdict{Tier: TierLossy, Reason: err.Error()}
	}
	return CheckSanitized(v, clean)
}

// CheckSanitized is Check for a value that has already been sanitized, so a
// caller holding the cleaned copy does not pay to sanitize it twice.
//
// v supplies the declared types for the loss walks; clean is what gets measured.
// Passing both is deliberate — sanitizing preserves concrete types, so the walks
// give the same answer either way, but the measurements must be taken on what
// will actually be emitted.
func CheckSanitized(v any, clean any) Verdict {
	// Two walks, because neither alone is sufficient. The type walk catches a
	// lossy type declared in an empty or nil container, which holds no values to
	// inspect. The value walk catches a lossy type reaching an `any` field,
	// whose static type says nothing about what it holds.
	if reason := lossyInType(reflect.TypeOf(v), map[reflect.Type]bool{}); reason != "" {
		return Verdict{Tier: TierLossy, Reason: reason}
	}
	if reason := lossyInValue(reflect.ValueOf(v), 0); reason != "" {
		return Verdict{Tier: TierLossy, Reason: reason}
	}

	// Measurements are taken on the SANITIZED value, because that is what Encode
	// emits. Judging the raw value made Check contradict Encode: a string with an
	// ANSI escape makes toon.Marshal fail, so Check called a perfectly good value
	// lossy — and advised "declare it as a type alias", which had nothing to do
	// with the cause. EncodeOrJSON then routed on that verdict and emitted a JSON
	// envelope for a value TOON handles fine.
	b, err := toon.Marshal(clean)
	if err != nil {
		// The alias advice is stated as a CONDITION, not as an instruction. It
		// used to be appended to every marshal error, so a func or a channel —
		// which sanitizeValue passes through untouched and which has no TOON
		// representation under any name — was told to rename itself. atcr's
		// review flagged the mismatch.
		return Verdict{
			Tier: TierLossy,
			Reason: fmt.Sprintf("%v; toon-go supports plain builtin types only — "+
				"where the type is a defined type over a builtin, declare it as a "+
				"type alias (=) instead", err),
		}
	}

	// Empty output is the correct answer for an empty input, and a fault for
	// anything else. Reporting every zero-length payload as broken would cry
	// wolf on legitimately empty results, which AXI asks to be stated plainly.
	if len(b) == 0 && !jsonProjectionIsEmpty(clean) {
		return Verdict{
			Tier:   TierLossy,
			Reason: "encoder produced empty output for a non-empty value",
		}
	}

	verdict := Verdict{OK: true, Tier: TierNested}
	if tabularHeader.Match(b) {
		verdict.Tier = TierTabular
	}

	jb, jerr := json.Marshal(clean)
	if jerr == nil {
		verdict.Efficient = len(b) <= len(jb)
		if !verdict.Efficient {
			verdict.Reason = fmt.Sprintf(
				"TOON is larger than JSON for this shape (%d vs %d bytes); prefer JSON for this command",
				len(b), len(jb))
		}
	}
	return verdict
}

// CanEncode reports whether v survives TOON encoding without losing data. It is
// the boolean form of Check for callers that do not need the detail.
func CanEncode(v any) bool { return Check(v).OK }

// lossyInType walks the declared type tree, returning a reason or "".
//
// This catches a lossy type declared inside an empty or nil container, which
// holds no values to inspect. Interface types are skipped deliberately: a static
// `any` says nothing about what it will hold, so only lossyInValue can judge it.
//
// The seen set makes recursive types terminate. Memoizing by type is sound here
// precisely because this walk never consults a value.
//
// This and lossyInValue look near-identical and must NOT be merged into one
// walker, which atcr's review raised. They differ in the one place that matters:
// this walk memoizes by type and lossyInValue cannot, because the same static
// type (`interface{}`) holds a different dynamic value at every position. A
// shared walker is exactly the bug that shipped once — one seen set covering
// both walks marked `interface{}` visited during the type pass, so every dynamic
// element in a []any was skipped and a TextMarshaler inside a slice or map went
// undetected. The duplication is the fix, not an oversight.
func lossyInType(t reflect.Type, seen map[reflect.Type]bool) string {
	if t == nil || seen[t] {
		return ""
	}
	if isLossyType(t) {
		return lossyReason(t)
	}
	seen[t] = true

	switch t.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array:
		return lossyInType(t.Elem(), seen)

	case reflect.Map:
		if reason := lossyInType(t.Key(), seen); reason != "" {
			return reason
		}
		return lossyInType(t.Elem(), seen)

	case reflect.Struct:
		for i := 0; i < t.NumField(); i++ {
			if t.Field(i).PkgPath != "" {
				continue // unexported; never encoded
			}
			if reason := lossyInType(t.Field(i).Type, seen); reason != "" {
				return reason
			}
		}
		return ""

	default:
		return ""
	}
}

// lossyInValue walks actual values, following interfaces to their dynamic type.
//
// It deliberately does NOT memoize by type. An earlier version shared one seen
// set between the type and value walks, and that silently defeated the whole
// check: for a []any the element's static type is `interface{}`, which the type
// walk had already marked, so every dynamic element was skipped and a
// TextMarshaler inside a slice or map went undetected.
//
// Cycles are bounded by depth instead, since a value cycle cannot be detected by
// type identity.
func lossyInValue(v reflect.Value, depth int) string {
	if !v.IsValid() || depth > maxWalkDepth {
		return ""
	}
	if isLossyType(v.Type()) {
		return lossyReason(v.Type())
	}

	switch v.Kind() {
	case reflect.Interface, reflect.Pointer:
		if v.IsNil() {
			return ""
		}
		return lossyInValue(v.Elem(), depth+1)

	case reflect.Struct:
		t := v.Type()
		for i := 0; i < t.NumField(); i++ {
			if t.Field(i).PkgPath != "" {
				continue
			}
			if reason := lossyInValue(v.Field(i), depth+1); reason != "" {
				return reason
			}
		}
		return ""

	case reflect.Map:
		if v.IsNil() {
			return ""
		}
		iter := v.MapRange()
		for iter.Next() {
			if reason := lossyInValue(iter.Key(), depth+1); reason != "" {
				return reason
			}
			if reason := lossyInValue(iter.Value(), depth+1); reason != "" {
				return reason
			}
		}
		return ""

	case reflect.Slice, reflect.Array:
		if v.Kind() == reflect.Slice && v.IsNil() {
			return ""
		}
		for i := 0; i < v.Len(); i++ {
			if reason := lossyInValue(v.Index(i), depth+1); reason != "" {
				return reason
			}
		}
		return ""

	default:
		return ""
	}
}

// nodeID identifies a reference-bearing value for the cycle walk.
//
// Pointers and maps are identified by address alone. A slice needs its length
// too: a slice and a sub-slice of it share a data pointer without either
// containing the other, so keying on the address alone reports a cycle for an
// ordinary `outer[1] = outer[:1]`. Including the length makes a repeat visit
// mean the identical slice, which is a genuine loop.
type nodeID struct {
	ptr uintptr
	len int
}

// hasCycle reports whether v refers to itself, directly or through other nodes.
//
// Pointers, maps AND slices are tracked by identity. A slice looks like it
// cannot close a loop on its own, and that is wrong: `s := make([]any, 1);
// s[0] = s` stores a header sharing its own backing array, and walking it
// recurses forever. Reproduced as a fatal stack overflow before slices were
// tracked here.
//
// The seen entry is removed on the way back out, so a node legitimately
// reachable by two different paths — a DAG, not a cycle — is not mistaken for
// one.
//
// There is deliberately NO depth cap. An earlier version gave up at
// maxWalkDepth and returned false, which made the guard a fiction: a chain of
// ~60 nodes closing back on itself was reported cycle-free, and sanitizeValue —
// which has neither a depth limit nor a seen-set — then walked it into the exact
// fatal stack overflow this function exists to prevent. Reproduced before the
// cap was removed. Termination does not need the cap: every edge that can repeat
// runs through a pointer, map or slice, and all three are in the seen set.
func hasCycle(v reflect.Value, seen map[nodeID]bool, depth int) bool {
	if !v.IsValid() {
		return false
	}

	switch v.Kind() {
	case reflect.Map, reflect.Pointer:
		if v.IsNil() {
			return false
		}
		id := nodeID{ptr: v.Pointer()}
		if seen[id] {
			return true
		}
		seen[id] = true
		defer delete(seen, id)

	case reflect.Slice:
		// An empty slice is not tracked. Zero-length allocations share one
		// address (runtime.zerobase), so tracking them collides unrelated
		// slices — and a slice with no elements cannot recurse anyway.
		if v.IsNil() || v.Len() == 0 {
			break
		}
		id := nodeID{ptr: v.Pointer(), len: v.Len()}
		if seen[id] {
			return true
		}
		seen[id] = true
		defer delete(seen, id)
	}

	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if v.IsNil() {
			return false
		}
		return hasCycle(v.Elem(), seen, depth+1)

	case reflect.Map:
		iter := v.MapRange()
		for iter.Next() {
			// KEYS are walked as well as values, and that is load-bearing. A map
			// key can be a pointer, and a pointer is comparable regardless of
			// what it points at, so a cycle can close entirely through keys.
			//
			// sanitizeValue recurses into keys with no seen-set of its own, so a
			// key-reachable cycle that this pre-check misses is not merely
			// undetected — it exhausts the stack and kills the process, which
			// recover() cannot catch. Reproduced exactly that way before this
			// line existed. The pre-check and the walker must traverse identical
			// edges or the guard is a fiction.
			if hasCycle(iter.Key(), seen, depth+1) {
				return true
			}
			if hasCycle(iter.Value(), seen, depth+1) {
				return true
			}
		}
		return false

	case reflect.Slice, reflect.Array:
		if v.Kind() == reflect.Slice && v.IsNil() {
			return false
		}
		for i := 0; i < v.Len(); i++ {
			if hasCycle(v.Index(i), seen, depth+1) {
				return true
			}
		}
		return false

	case reflect.Struct:
		t := v.Type()
		for i := 0; i < t.NumField(); i++ {
			if t.Field(i).PkgPath != "" {
				continue
			}
			if hasCycle(v.Field(i), seen, depth+1) {
				return true
			}
		}
		return false

	default:
		return false
	}
}

// jsonProjectionIsEmpty reports whether v genuinely has nothing to emit, so empty
// output is the correct answer rather than a fault.
//
// The question is asked of the JSON projection rather than by counting struct
// fields. Counting was wrong: a struct whose only field is `omitempty` and empty
// has one field and still encodes to zero bytes, so Check called a perfectly
// good value lossy.
//
// Deferring to encoding/json is safe here even though toon-go does not share its
// tag names, because emptiness does not depend on what the keys are called, and
// both encoders honour omitempty identically (verified by probe). A
// TextMarshaler would fool this test, but it is already rejected by the two
// lossy walks before this point is reached.
func jsonProjectionIsEmpty(v any) bool {
	if v == nil {
		return true
	}
	b, err := json.Marshal(v)
	if err != nil {
		return false // cannot tell; treat as having content
	}
	switch strings.TrimSpace(string(b)) {
	case "{}", "[]", "null", `""`:
		return true
	default:
		return false
	}
}

// typeName renders a type for an error message, falling back to its string form
// for anonymous types.
func typeName(t reflect.Type) string {
	if n := t.Name(); n != "" {
		return n
	}
	return strings.TrimSpace(t.String())
}
