package goaxi

import (
	"fmt"
	"reflect"
	"strings"
	"unicode"
	"unicode/utf8"
)

// KeyCollisionError reports two map keys that became identical after cleaning.
//
// This is the one case where sanitizing cannot proceed without losing data.
// "na\x1bme" and "name" are different keys on input and the same key after the
// control byte is removed, so one would overwrite the other. Go randomizes map
// iteration order, which makes the survivor vary between runs — the worst kind
// of loss, because a test can pass today and fail tomorrow with no code change.
//
// Sanitize refuses rather than picking. Renaming one key would invent data the
// caller never wrote, and dropping one is the silent loss this module exists to
// prevent.
type KeyCollisionError struct {
	Cleaned any // the key both inputs produced
	First   any // the key already present
	Second  any // the key that would have overwritten it
}

func (e *KeyCollisionError) Error() string {
	return fmt.Sprintf(
		"goaxi: map keys %#v and %#v both sanitize to %#v; "+
			"one would silently overwrite the other, so the value is refused rather than guessed",
		e.First, e.Second, e.Cleaned)
}

// CycleError reports a value that refers to itself, directly or through other
// nodes.
//
// TOON has no way to express a back-reference, so there is no correct encoding
// to fall back to. The walk is refused before it is entered rather than bounded
// part way in: sanitizeValue recurses through pointers with no seen-set, and the
// resulting stack overflow is a fatal runtime error, not a panic. No recover()
// catches it, so the process dies with a goroutine dump instead of returning to
// the caller — the exact class of failure this module exists to convert into an
// error.
//
// Check already refuses a cycle for this reason. Sanitize now agrees, so the
// guarantee does not depend on which entry point a caller happened to reach for.
type CycleError struct {
	Type string // the type the cycle was detected in
}

func (e *CycleError) Error() string {
	return fmt.Sprintf(
		"goaxi: value of type %s contains a reference cycle; TOON cannot represent one, "+
			"and walking it would exhaust the stack and kill the process", e.Type)
}

// Sanitize returns a copy of v with every string cleaned of characters that
// would either break the TOON encoder or reach stdout as a raw control byte.
//
// It exists because toon-go mishandles hostile input in two opposite
// directions, both confirmed against the pinned version rather than assumed:
//
//   - A raw ANSI escape (\x1b) makes toon.Marshal return an error, so a command
//     handed reviewer text containing colour codes emits nothing at all.
//   - U+2028, U+2029, lone C1 bytes (0x9b, 0x9d) and invalid UTF-8 pass through
//     untouched, reaching whatever terminal renders the output.
//
// Sanitizing before encoding fixes both: the encoder never sees a byte it would
// reject, and never sees one it would forward.
//
// Characters are STRIPPED rather than replaced, so text on either side of a
// removed byte joins contiguously. Substituting a space would silently alter
// content; a reader cannot tell an inserted space from a real one.
//
// Tab, carriage return and newline are deliberately preserved. toon-go already
// escapes those correctly, and removing them would corrupt multi-line content.
//
// The copy preserves concrete types, so struct tags survive. That matters more
// than it looks: TOON field names are part of the contract consuming tools
// parse, and flattening a struct to map[string]any would rename every column to
// its Go identifier. Strings inside unexported fields cannot be reached by
// reflection and are carried through as-is.
//
// The input is never MUTATED. What is not promised is that the result occupies
// different memory: nothing is copied unless a string actually needed cleaning,
// so for a clean value the result IS the input. A caller that mutates its own
// value after calling Sanitize should not expect the returned value to stay
// frozen. This was already true before the copy became conditional — nil
// containers, scalars, funcs, channels and every unexported struct field were
// always passed through by reference.
//
// Two errors are returned: *CycleError for a self-referential value, and
// *KeyCollisionError for two map keys that clean to the same string. A caller
// whose keys are fixed identifiers and whose shapes are acyclic can rule both
// out and use MustSanitize.
func Sanitize(v any) (any, error) {
	if v == nil {
		return nil, nil
	}
	rv := reflect.ValueOf(v)
	// Refuse before walking, not during. sanitizeValue follows pointers with no
	// seen-set, so a cycle never reaches the error return — it exhausts the
	// stack and kills the process. This reuses the detector Check uses, so the
	// two entry points cannot disagree about what counts as a cycle.
	if hasCycle(rv, map[nodeID]bool{}, 0) {
		return nil, &CycleError{Type: typeName(reflect.TypeOf(v))}
	}
	out, _, err := sanitizeValue(rv)
	if err != nil {
		return nil, err
	}
	return out.Interface(), nil
}

// MustSanitize is Sanitize for callers whose map keys are fixed identifiers and
// whose shapes are acyclic, where both failures are impossible by construction.
// It panics if one occurs, because continuing would mean emitting a payload with
// a field missing.
//
// A panic is the lesser evil for a cycle specifically: it unwinds and can be
// recovered, whereas the stack overflow it replaces is fatal and cannot.
func MustSanitize(v any) any {
	out, err := Sanitize(v)
	if err != nil {
		panic(err)
	}
	return out
}

// SanitizeString cleans a single string using the same rules as Sanitize. It is
// exported for callers that build output a field at a time rather than handing
// over a whole value. It cannot collide, so it returns no error.
func SanitizeString(s string) string {
	return cleanString(s)
}

// needsCleaning reports whether any string at or below v would change, without
// building anything.
//
// This is the gate that stops a clean container being rebuilt. It exists because
// the obvious alternative — recursing sanitizeValue and discarding the result
// when nothing changed — allocates the very copies it throws away. Measured on a
// dirty payload, that cost 48 wasted allocations per row against 0 for this
// predicate, which exits at the first string needing work and so costs the same
// whatever the payload size.
//
// On a CLEAN payload it measures 6 allocations per row, identical to hasCycle
// and to sanitizeValue's own walk. That figure is the reflect map-iteration
// boxing any walk of a map[string]any must pay; it is the floor, not overhead
// this adds.
//
// No seen set is needed. Inside Sanitize this runs only after hasCycle has
// refused every cycle, so the value is acyclic by the time it is reached.
// sanitizeValue has exactly one non-recursive caller, Sanitize, and the cycle
// guard runs three lines above it — so there is no path here that skips it.
//
// COST, stated because it is a real trade and not an oversight. The map and
// struct branches of sanitizeValue each call this on their own subtree, so
// detection runs once per container LEVEL: O(n × depth), not O(n). Measured on
// nested maps with the only dirty string at the deepest leaf — the worst case,
// since every level must scan all the way down before it can answer:
//
//	depth    allocations
//	10       302
//	50       3,690
//	100      12,992
//	200      48,435
//
// Doubling the depth roughly quadruples the cost. The alternative is to gate
// once at the root, which is O(n) — but then ANY dirty string rebuilds the whole
// payload. Per-container gating costs 9,064 allocations for one dirty row in
// 500; rebuilding everything costs about 15,500, and root gating would pay that
// plus detection. So the level-by-level gate is what makes a mostly-clean
// payload cheap, which is the case this package exists to serve.
//
// The quadratic shape is accepted because it needs nested maps hundreds of
// levels deep. TOON payloads are rows, two to four levels deep, and the
// structure comes from the tool rather than from untrusted input — so this is
// not reachable by anything a caller feeds in. At depth 10 it costs 302
// allocations. Correctness at depth is pinned by
// TestSanitize_DeeplyNestedDirtyValueIsStillCleaned.
func needsCleaning(v reflect.Value) bool {
	if !v.IsValid() {
		return false
	}

	switch v.Kind() {
	case reflect.String:
		// cleanString returns its argument unchanged when there is nothing to
		// strip, so this comparison is a pointer check in the common case.
		s := v.String()
		return cleanString(s) != s

	case reflect.Interface, reflect.Pointer:
		if v.IsNil() {
			return false
		}
		return needsCleaning(v.Elem())

	case reflect.Struct:
		t := v.Type()
		for i := 0; i < t.NumField(); i++ {
			if t.Field(i).PkgPath != "" {
				continue // unexported; unreachable by reflection, so unchangeable
			}
			if needsCleaning(v.Field(i)) {
				return true
			}
		}
		return false

	case reflect.Map:
		if v.IsNil() {
			return false
		}
		for iter := v.MapRange(); iter.Next(); {
			// Keys count. A key is a field name in tabular output, so a control
			// byte there lands in the header rather than in a cell.
			if needsCleaning(iter.Key()) || needsCleaning(iter.Value()) {
				return true
			}
		}
		return false

	case reflect.Slice, reflect.Array:
		if v.Kind() == reflect.Slice && v.IsNil() {
			return false
		}
		for i := 0; i < v.Len(); i++ {
			if needsCleaning(v.Index(i)) {
				return true
			}
		}
		return false

	default:
		// Numbers, bools, funcs, channels: nothing to clean.
		return false
	}
}

// sanitizeValue walks v and returns a cleaned value of the same type, plus
// whether anything at or below it actually changed.
//
// Nothing is allocated unless a string needed cleaning. Every branch used to
// rebuild its node unconditionally, which cost 25 of the 31 allocations per row
// Sanitize spent producing a result identical to its input — 80.6% of the total
// at 500 rows. When a branch reports changed=false its caller passes the
// original value straight through, so a clean payload allocates nothing here.
//
// No branch ever MUTATES v. That guarantee is what callers rely on and it is
// unchanged; a sanitizer with a side effect on its argument is a trap, because a
// caller that later writes the same value as JSON would silently get the
// stripped version. What is no longer promised is that the result occupies
// different memory — see Sanitize's doc comment.
//
// The map and struct branches gate on needsCleaning rather than recursing to
// find out whether anything changed. An earlier version of this did recurse and
// discard the resulting copies, which made a fully dirty payload 1.69x more
// expensive than before copy-on-write existed — it allocated the rebuild twice.
//
// The gate applies at every level, not only at the root, so a clean subtree
// inside a dirty payload is also passed through rather than rebuilt. One dirty
// row in 500 costs about a third of what rebuilding all 500 would.
func sanitizeValue(v reflect.Value) (reflect.Value, bool, error) {
	switch v.Kind() {
	case reflect.String:
		// cleanString returns its argument unchanged when there is nothing to
		// strip, so this comparison is a pointer check in the common case.
		cleaned := cleanString(v.String())
		if cleaned == v.String() {
			return v, false, nil
		}
		out := reflect.New(v.Type()).Elem()
		out.SetString(cleaned)
		return out, true, nil

	case reflect.Interface:
		if v.IsNil() {
			return v, false, nil
		}
		inner, changed, err := sanitizeValue(v.Elem())
		if err != nil {
			return reflect.Value{}, false, err
		}
		if !changed {
			return v, false, nil
		}
		out := reflect.New(v.Type()).Elem()
		out.Set(inner)
		return out, true, nil

	case reflect.Pointer:
		if v.IsNil() {
			return v, false, nil
		}
		inner, changed, err := sanitizeValue(v.Elem())
		if err != nil {
			return reflect.Value{}, false, err
		}
		if !changed {
			return v, false, nil
		}
		out := reflect.New(v.Type().Elem())
		out.Elem().Set(inner)
		return out, true, nil

	case reflect.Slice:
		if v.IsNil() {
			return v, false, nil
		}
		// Allocate on the FIRST changed element and bulk-copy the original, then
		// overwrite only what changed. Same shape cleanString uses for strings:
		// scan, and copy just once something has to move.
		var out reflect.Value
		for i := 0; i < v.Len(); i++ {
			elem, changed, err := sanitizeValue(v.Index(i))
			if err != nil {
				return reflect.Value{}, false, err
			}
			if !changed {
				continue
			}
			if !out.IsValid() {
				out = reflect.MakeSlice(v.Type(), v.Len(), v.Len())
				reflect.Copy(out, v)
			}
			out.Index(i).Set(elem)
		}
		if !out.IsValid() {
			return v, false, nil
		}
		return out, true, nil

	case reflect.Array:
		var out reflect.Value
		for i := 0; i < v.Len(); i++ {
			elem, changed, err := sanitizeValue(v.Index(i))
			if err != nil {
				return reflect.Value{}, false, err
			}
			if !changed {
				continue
			}
			if !out.IsValid() {
				out = reflect.New(v.Type()).Elem()
				out.Set(v)
			}
			out.Index(i).Set(elem)
		}
		if !out.IsValid() {
			return v, false, nil
		}
		return out, true, nil

	case reflect.Map:
		if v.IsNil() {
			return v, false, nil
		}
		// A clean map must allocate neither a replacement map nor the
		// collision-tracking set, and needsCleaning answers that without
		// building anything.
		//
		// No error can be missed by gating here. The only error this branch
		// raises is *KeyCollisionError, which requires a key that CHANGES when
		// cleaned — so the predicate reports true for any map that could collide,
		// and the build pass below still surfaces it.
		if !needsCleaning(v) {
			return v, false, nil
		}

		// Keys are sanitized too. A key is a field name in tabular output, so a
		// control byte there lands in the header rather than a cell.
		//
		// Cleaning can make two distinct keys identical, so collisions are
		// tracked and refused. Map keys are always comparable, so using the
		// cleaned key in a lookup map is safe.
		//
		// Reached only when something changed. A map whose keys all cleaned to
		// themselves cannot collide, because they were distinct to begin with.
		out := reflect.MakeMapWithSize(v.Type(), v.Len())
		seen := make(map[any]any, v.Len())
		for iter := v.MapRange(); iter.Next(); {
			key, _, err := sanitizeValue(iter.Key())
			if err != nil {
				return reflect.Value{}, false, err
			}
			val, _, err := sanitizeValue(iter.Value())
			if err != nil {
				return reflect.Value{}, false, err
			}
			cleaned := key.Interface()
			if first, dup := seen[cleaned]; dup {
				return reflect.Value{}, false, &KeyCollisionError{
					Cleaned: cleaned,
					First:   first,
					Second:  iter.Key().Interface(),
				}
			}
			seen[cleaned] = iter.Key().Interface()
			out.SetMapIndex(key, val)
		}
		return out, true, nil

	case reflect.Struct:
		t := v.Type()
		// Same gate as the map branch: a clean struct must not pay for
		// reflect.New. time.Time is the case that proves it matters — it is
		// entirely unexported state, so no field can change and it passes
		// straight through untouched.
		if !needsCleaning(v) {
			return v, false, nil
		}

		// Copy wholesale first so unexported fields survive, then overwrite the
		// exported ones. reflect can set a whole struct value but not an
		// individual unexported field, which is why the order matters.
		out := reflect.New(v.Type()).Elem()
		out.Set(v)
		for i := 0; i < t.NumField(); i++ {
			if t.Field(i).PkgPath != "" {
				continue
			}
			// No CanSet guard: out came from reflect.New(...).Elem() so it is
			// addressable, and unexported fields were skipped above, so every
			// field reaching here is settable by construction.
			field, _, err := sanitizeValue(v.Field(i))
			if err != nil {
				return reflect.Value{}, false, err
			}
			out.Field(i).Set(field)
		}
		return out, true, nil

	default:
		// Numbers, bools, funcs, channels: nothing to clean.
		return v, false, nil
	}
}

// cleanString drops unsafe runes and invalid UTF-8 bytes.
//
// The scan is manual rather than a range loop because range cannot distinguish a
// genuine U+FFFD in the input from an invalid byte decoded as one. A lone 0x9b
// must be dropped; a real replacement character the caller supplied must not be.
func cleanString(s string) string {
	// One pass. Scan until the first byte that has to go, and only then allocate
	// and copy. A clean string — the common case by far — is walked exactly once
	// and returns itself.
	//
	// The previous version asked isClean first, which cost a full utf8.ValidString
	// scan plus a full range loop, then scanned a third time to rebuild a dirty
	// string. atcr's review flagged it as a double scan; it was worse than that.
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if (r == utf8.RuneError && size == 1) || unsafeRune(r) {
			return cleanFrom(s, i)
		}
		i += size
	}
	return s
}

// cleanFrom builds the cleaned result for a string already known to be dirty at
// byte offset i. The prefix before i has been verified clean, so it is copied
// wholesale rather than re-examined rune by rune.
func cleanFrom(s string, i int) string {
	var b strings.Builder
	b.Grow(len(s))
	b.WriteString(s[:i])
	for i < len(s) {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			i++ // invalid byte: drop it
			continue
		}
		if !unsafeRune(r) {
			b.WriteRune(r)
		}
		i += size
	}
	return b.String()
}

// unsafeRune reports whether r must not reach TOON output.
//
// U+2028 and U+2029 are checked explicitly because they are Unicode separators,
// not "control" characters, so unicode.IsControl returns false for both. A
// sanitizer relying on IsControl alone passes them straight through — which is
// exactly what toon-go does today.
func unsafeRune(r rune) bool {
	switch r {
	case '\n', '\r', '\t':
		return false // valid TOON escapes; toon-go handles these correctly
	case '\u2028', '\u2029':
		return true
	}
	return unicode.IsControl(r)
}
