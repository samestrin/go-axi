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
	// Only meaningful when SizeCompared is true.
	Efficient bool

	// SizeCompared reports whether Efficient was actually measured. The
	// comparison needs a second marshal, so a path that writes the payload and
	// never reads the cost signal skips it. Without this flag a skipped
	// comparison is indistinguishable from a measured "TOON is larger".
	SizeCompared bool

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
	verdict, _ := verdictFor(v, clean, true)
	return verdict
}

// verdictFor is the single pass behind CheckSanitized and EncodeChecked.
//
// It returns the verdict AND the TOON bytes the verdict was derived from, so a
// caller that intends to write those bytes does not marshal the same value a
// second time. Check followed by Encode used to do exactly that — two sanitize
// walks and two marshals for one guard, 2.27x the allocations of encoding alone
// on a 2000-row payload (222,204 against 98,073).
//
// sizeCompare gates the TOON-versus-JSON measurement, which costs another
// marshal and is only read by a caller asking for the cost signal.
func verdictFor(v any, clean any, sizeCompare bool) (Verdict, []byte) {
	// Two walks, because neither alone is sufficient. The type walk catches a
	// lossy type declared in an empty or nil container, which holds no values to
	// inspect. The value walk catches a lossy type reaching an `any` field,
	// whose static type says nothing about what it holds.
	if reason := lossyInType(reflect.TypeOf(v), map[reflect.Type]bool{}); reason != "" {
		return Verdict{Tier: TierLossy, Reason: reason}, nil
	}
	if reason := fastLossyInValue(v, map[nodeID]bool{}); reason != "" {
		return Verdict{Tier: TierLossy, Reason: reason}, nil
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
		}, nil
	}

	// Empty output is the correct answer for an empty input, and a fault for
	// anything else. Reporting every zero-length payload as broken would cry
	// wolf on legitimately empty results, which AXI asks to be stated plainly.
	if len(b) == 0 && !jsonProjectionIsEmpty(clean) {
		return Verdict{
			Tier:   TierLossy,
			Reason: "encoder produced empty output for a non-empty value",
		}, nil
	}

	verdict := Verdict{OK: true, Tier: TierNested}
	if tabularHeader.Match(b) {
		verdict.Tier = TierTabular
	}

	if sizeCompare {
		jb, jerr := json.Marshal(clean)
		if jerr == nil {
			verdict.SizeCompared = true
			verdict.Efficient = len(b) <= len(jb)
			if !verdict.Efficient {
				verdict.Reason = fmt.Sprintf(
					"TOON is larger than JSON for this shape (%d vs %d bytes); prefer JSON for this command",
					len(b), len(jb))
			}
		}
	}
	return verdict, b
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
// It DOES memoize by NODE IDENTITY, which is sound where type keying was not. A
// pointer, map or slice node is the same subtree every time it is reached, so if
// the first full visit found no loss, no later visit can. That is what
// terminates the walk.
//
// The memo is BOUNDED. Entries are kept exactly as before while the map is
// small; past maxLossyMemo a node is still inserted on the way IN — so it guards
// the subtree below it against a cycle — but deleted on the way OUT rather than
// kept. Peak size is maxLossyMemo + depth instead of one entry per node, and a
// payload below the bound pays nothing for it. See maxLossyMemo.
//
// Termination used to be a depth cap, and the cap was a hole. It returned "" at
// depth 100, and "" MEANS "no loss found" — so a TextMarshaler nested deeper was
// reported clean while toon-go dropped its value and still emitted its key.
// Probed against the pinned version, the boundary was exact: 49 layers of []any
// were caught, 50 were not, nor 200, nor 1000.
//
// The cap could not simply be deleted. CheckSanitized is public and runs this
// walk on a RAW value with no cycle pre-check of its own, so a self-referential
// argument would recurse until the stack died — a fatal runtime error rather
// than a panic, which recover() cannot catch. The seen set replaces the cap
// without leaving that hole: termination no longer depends on how deep the value
// is, only on whether a node repeats.
func lossyInValue(v reflect.Value, seen map[nodeID]bool) string {
	if !v.IsValid() {
		return ""
	}
	if isLossyType(v.Type()) {
		return lossyReason(v.Type())
	}

	// Identity is tracked for the three reference-bearing kinds, matching inspect
	// so the two walks cannot disagree about which edges can repeat.
	//
	// Unlike inspect the entry is NOT removed on the way back out, and that
	// difference is why the two cannot share one set. inspect asks "is this node
	// on my current path", which requires popping or every DAG looks like a
	// cycle; this walk asks "is there a loss anywhere below", so a node already
	// proven clean stays clean however it is reached again and the entry is kept
	// as a memo. Opposite policies, deliberately separate walks.
	var (
		id      nodeID
		tracked bool
	)
	switch v.Kind() {
	case reflect.Map, reflect.Pointer:
		if v.IsNil() {
			return ""
		}
		id, tracked = nodeID{ptr: v.Pointer()}, true

	case reflect.Slice:
		// An empty slice is not tracked. Zero-length allocations share one
		// address (runtime.zerobase), so tracking them would collide unrelated
		// slices — and a slice with no elements has nothing below it anyway.
		if v.IsNil() || v.Len() == 0 {
			break
		}
		id, tracked = nodeID{ptr: v.Pointer(), len: v.Len()}, true
	}
	if tracked {
		if seen[id] {
			return ""
		}
		seen[id] = true
		if len(seen) > maxLossyMemo {
			// Past the bound this entry is transient. It still guards the
			// subtree below against a cycle, but it is dropped on the way out
			// instead of being kept as a memo. See maxLossyMemo.
			defer delete(seen, id)
		}
	}

	switch v.Kind() {
	case reflect.Interface, reflect.Pointer:
		if v.IsNil() {
			return ""
		}
		return lossyInValue(v.Elem(), seen)

	case reflect.Struct:
		t := v.Type()
		for i := 0; i < t.NumField(); i++ {
			if t.Field(i).PkgPath != "" {
				continue
			}
			if reason := lossyInValue(v.Field(i), seen); reason != "" {
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
			if reason := lossyInValue(iter.Key(), seen); reason != "" {
				return reason
			}
			if reason := lossyInValue(iter.Value(), seen); reason != "" {
				return reason
			}
		}
		return ""

	case reflect.Slice, reflect.Array:
		if v.Kind() == reflect.Slice && v.IsNil() {
			return ""
		}
		for i := 0; i < v.Len(); i++ {
			if reason := lossyInValue(v.Index(i), seen); reason != "" {
				return reason
			}
		}
		return ""

	default:
		return ""
	}
}

// maxLossyMemo bounds how many nodes the lossy walk remembers.
//
// Without a bound the walk retained one entry per distinct pointer, map and
// slice for its whole duration — O(total nodes) live, where the depth cap it
// replaced was O(depth). Measured before the bound: 50,002 entries for a 50,000
// row listing, on the output path through EncodeChecked and EncodeOrJSON, so a
// million-row listing built a multi-million-entry map on top of the payload.
//
// The memo cannot simply be removed, because it is also what TERMINATES the
// walk: a cycle is caught by arriving at a node already in the map.
//
// So entries are kept exactly as before while the map is small, and only past
// this size does an entry become transient — still inserted on the way in, so it
// guards the subtree below it against a cycle, but deleted on the way out
// instead of being kept. Peak size is maxLossyMemo plus the depth of the value.
//
// PAYING NOTHING UNTIL THE BOUND IS REACHED is the point, and it was not free to
// learn. The first version tracked path and cleared nodes as two states and did
// bookkeeping on every node regardless of size. It cost 7.7% on EncodeOrJSON at
// 2000 rows and 4.3% on Check — a permanent tax on every payload, to bound a
// case that only arises far above that size. Measured, then replaced with this.
//
// Dropping an entry costs time, never correctness: the node is walked again and
// reaches the same answer. 4096 is generous for the sharing a TOON payload
// actually contains — row listings share almost nothing — while keeping the
// worst case to kilobytes rather than to the size of the input.
const maxLossyMemo = 4096

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

// inspect walks v ONCE and answers both questions Sanitize must ask before it
// can act: does the value refer to itself, and does anything in it need
// cleaning.
//
// Those two checks visit exactly the same nodes across exactly the same edges,
// so running them as separate passes walked every payload twice — 12 allocations
// per row on a clean listing where a single walk costs 6. Merging them also
// retires a standing hazard an external review named: separate walkers whose
// edge sets had to stay identical forever, enforced by nothing but comments and
// the hope that whoever edits one remembers the others.
//
// CYCLES. Pointers, maps AND slices are tracked by identity. A slice looks like
// it cannot close a loop on its own, and that is wrong: `s := make([]any, 1);
// s[0] = s` stores a header sharing its own backing array, and walking it
// recurses forever. Reproduced as a fatal stack overflow before slices were
// tracked here.
//
// The path entry is removed on the way back out, so a node legitimately
// reachable by two different routes — a DAG, not a cycle — is not mistaken for
// one. That popping is also why this cannot share its set with lossyInValue,
// which KEEPS entries in order to memoize subtrees it has already cleared. The
// two need opposite policies, so they stay separate on purpose; merging those
// would silently break one of them.
//
// There is deliberately NO depth cap. An earlier version gave up at a fixed
// depth and returned false, which made the guard a fiction: a chain of ~60 nodes
// closing back on itself was reported cycle-free, and sanitizeValue — which has
// neither a depth limit nor a seen-set — then walked it into the exact fatal
// stack overflow this exists to prevent. Termination does not need a cap: every
// edge that can repeat runs through a pointer, map or slice, and all three are
// tracked.
//
// THE WALK MUST NOT SHORT-CIRCUIT ON DIRTINESS. Returning the moment a string is
// found to need cleaning would leave the rest of the value unvisited, so a cycle
// further along would go undetected, Sanitize would proceed, and sanitizeValue
// would exhaust the stack — fatal, and recover() cannot catch it. Only a CYCLE
// may return early, because Sanitize refuses the value outright in that case and
// never reads the flag.
//
// The flag is threaded by POINTER rather than returned, so that once dirtiness is
// established the string nodes stop calling cleanString while the walk carries on
// visiting every edge. Accumulating it as a return value instead cost 1,500
// redundant string scans on a fully dirty 500-row listing — sanitizeValue scans
// each string again during the rebuild — and measured 602us against 350us for
// this form, with allocations unchanged. The answer is identical either way; only
// the wasted scanning differs.
func inspect(v reflect.Value, path map[nodeID]bool, dirty *bool) (cycle bool) {
	if !v.IsValid() {
		return false
	}

	switch v.Kind() {
	case reflect.Map, reflect.Pointer:
		if v.IsNil() {
			return false
		}
		id := nodeID{ptr: v.Pointer()}
		if path[id] {
			return true
		}
		path[id] = true
		defer delete(path, id)

	case reflect.Slice:
		// An empty slice is not tracked. Zero-length allocations share one
		// address (runtime.zerobase), so tracking them collides unrelated
		// slices — and a slice with no elements cannot recurse anyway.
		if v.IsNil() || v.Len() == 0 {
			break
		}
		id := nodeID{ptr: v.Pointer(), len: v.Len()}
		if path[id] {
			return true
		}
		path[id] = true
		defer delete(path, id)
	}

	switch v.Kind() {
	case reflect.String:
		// Skipped once the answer is already known — see the pointer note above.
		// cleanString returns its argument unchanged when there is nothing to
		// strip, so this comparison is a pointer check in the common case.
		if !*dirty {
			s := v.String()
			if cleanString(s) != s {
				*dirty = true
			}
		}
		return false

	case reflect.Pointer, reflect.Interface:
		if v.IsNil() {
			return false
		}
		return inspect(v.Elem(), path, dirty)

	case reflect.Map:
		iter := v.MapRange()
		for iter.Next() {
			// KEYS are walked as well as values, and that is load-bearing twice
			// over. A map key can be a pointer, and a pointer is comparable
			// regardless of what it points at, so a cycle can close entirely
			// through keys. A key is also a field name in tabular output, so a
			// control byte there lands in the header rather than in a cell.
			//
			// sanitizeValue recurses into keys with no seen-set of its own, so a
			// key-reachable cycle missed here is not merely undetected — it
			// exhausts the stack and kills the process, which recover() cannot
			// catch. Reproduced exactly that way before keys were walked.
			if inspect(iter.Key(), path, dirty) {
				return true
			}
			if inspect(iter.Value(), path, dirty) {
				return true
			}
		}
		return false

	case reflect.Slice, reflect.Array:
		if v.Kind() == reflect.Slice && v.IsNil() {
			return false
		}
		for i := 0; i < v.Len(); i++ {
			if inspect(v.Index(i), path, dirty) {
				return true
			}
		}
		return false

	case reflect.Struct:
		t := v.Type()
		for i := 0; i < t.NumField(); i++ {
			if t.Field(i).PkgPath != "" {
				continue // unexported; unreachable by reflection, so unchangeable
			}
			if inspect(v.Field(i), path, dirty) {
				return true
			}
		}
		return false

	default:
		// Numbers, bools, funcs, channels: nothing to clean, nothing to loop.
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
