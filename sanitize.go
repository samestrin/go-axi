package goaxi

import (
	"fmt"
	"reflect"
	"strings"
	"sync"
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

// Error quotes both original keys with %#v, so the invisible byte that made
// them collide is readable in a terminal that would not render it.
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

// Error names the type the cycle was found in, and says why the value is
// refused rather than encoded.
func (e *CycleError) Error() string {
	return fmt.Sprintf(
		"goaxi: value of type %s contains a reference cycle; TOON cannot represent one, "+
			"and walking it would exhaust the stack and kill the process", e.Type)
}

// pathPool recycles the identity set fastInspect tracks the current path in.
//
// It MUST be handed back empty. The set means "these nodes are on the path I am
// walking now", so a stale entry makes the next walk that draws this map report
// a cycle for an acyclic payload — and MustSanitize turns that into a panic.
// Sanitize clears it on the cycle branch, where fastInspect unwinds without
// popping; on the clean branch every frame has already popped its own entry.
//
// Declared ABOVE Sanitize's doc comment on purpose. A package-level declaration
// sitting between a comment and the function it describes reattaches the whole
// comment to that declaration, which compiles, vets and reads correctly while
// rendering the function undocumented. That happened here and cost Sanitize all
// 60 lines below, including the concurrency hazard.
// TestExportedSymbolsAreDocumented now fails if it happens again.
var pathPool = sync.Pool{
	New: func() any {
		return make(map[nodeID]bool, 16)
	},
}

// Sanitize returns v with every string cleaned of characters that would either
// break the TOON encoder or reach stdout as a raw control byte.
//
// # Memory
//
// The input is never MUTATED, and that is the guarantee callers rely on. What is
// NOT promised is that the result occupies different memory: nothing is allocated
// unless a string actually needed cleaning, so for a clean value the result IS v.
// This was already true before the copy became conditional — nil containers,
// scalars, funcs, channels and every unexported struct field were always passed
// through by reference.
//
// Two rules follow, and a caller needs both:
//
//   - Do not mutate your own value after calling Sanitize and expect the
//     returned value to stay as it was. There is no compile-time or runtime
//     signal that the two are the same value.
//
//   - Do not mutate it CONCURRENTLY while this call runs, or while the result is
//     being encoded. Encode, EncodeChecked and EncodeOrJSON pass the result
//     straight to toon.Marshal, so a clean payload is marshalled from the
//     caller's own map rather than from a private snapshot. A concurrent write
//     during that window is a fatal "concurrent map read and map write" that
//     recover() cannot catch. Rebuilding unconditionally used to hide that race
//     behind a copy; it never made the caller's code safe, because encoding/json
//     and every other reflective marshaller carry the identical hazard.
//
// # Why it exists
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
// Concrete types are preserved, so struct tags survive. That matters more than
// it looks: TOON field names are part of the contract consuming tools parse, and
// flattening a struct to map[string]any would rename every column to its Go
// identifier. Strings inside unexported fields cannot be reached by reflection
// and are carried through as-is.
//
// Two errors are returned: *CycleError for a self-referential value, and
// *KeyCollisionError for two map keys that clean to the same string. A caller
// whose keys are fixed identifiers and whose shapes are acyclic can rule both
// out and use MustSanitize.
func Sanitize(v any) (any, error) {
	if v == nil {
		return nil, nil
	}

	// Fast path for leaf scalars and plain strings: no map allocation, no reflection.
	switch x := v.(type) {
	case string:
		cleaned := cleanString(x)
		if cleaned == x {
			return v, nil
		}
		return cleaned, nil
	case int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64,
		float32, float64, bool:
		return v, nil
	}

	var dirty bool
	path := pathPool.Get().(map[nodeID]bool)
	if fastInspect(v, path, &dirty) {
		clear(path)
		pathPool.Put(path)
		return nil, &CycleError{Type: typeName(reflect.TypeOf(v))}
	}
	pathPool.Put(path)
	if !dirty {
		return v, nil
	}
	rv := reflect.ValueOf(v)
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

// needsCleaning reports whether any string at or below v would change.
//
// A thin wrapper over inspect, which answers this and the cycle question in one
// traversal. Production code does not call it — Sanitize takes both answers from
// inspect directly rather than walking the value twice.
//
// It stays because the agreement between this predicate and sanitizeValue is the
// load-bearing invariant of the whole copy-on-write scheme: if it ever answers
// "no" for a value sanitizing WOULD have changed, a control byte reaches output
// and this package has failed at its one job. Naming it keeps that property
// testable on its own, rather than only through its effect on Sanitize. See
// TestSanitize_PredicateAgreesWithTheWalk.
// The cycle result is discarded deliberately: every value reaching this in a test
// is acyclic, and a cyclic one is Sanitize's problem rather than the predicate's.
func needsCleaning(v reflect.Value) bool {
	var dirty bool
	inspect(v, map[nodeID]bool{}, &dirty)
	return dirty
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
// This is reached only when Sanitize's root gate has already established that
// something in the payload needs cleaning, so every branch rebuilds rather than
// re-asking. The changed flag still earns its place below the containers: a
// string that cleans to itself, a nil pointer or an untouched slice element
// returns the original, so the rebuild copies only what actually moved.
//
// Do NOT reintroduce a per-container needsCleaning gate to skip clean subtrees.
// It looks like an obvious win and it is not: each gate re-scans its whole
// subtree, which makes the walk O(n × depth) and cost 3.97 seconds on a value
// nested 10,000 deep. Measured, then removed.
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
		// Keys are sanitized too. A key is a field name in tabular output, so a
		// control byte there lands in the header rather than a cell.
		//
		// Cleaning can make two distinct keys identical, so collisions are
		// tracked and refused. Map keys are always comparable, so using the
		// cleaned key in a lookup map is safe.
		//
		// The check cannot be skipped for a map whose own keys happen to be
		// clean: this branch runs whenever ANYTHING in the payload was dirty, not
		// only when this map was. A map whose keys all clean to themselves cannot
		// collide — they were distinct to begin with — so the tracking simply
		// finds nothing, which costs a little and guarantees the rest.
		out := reflect.MakeMapWithSize(v.Type(), v.Len())
		seen := make(map[any]any, v.Len())
		for iter := v.MapRange(); iter.Next(); {
			key, keyChanged, err := sanitizeValue(iter.Key())
			if err != nil {
				return reflect.Value{}, false, err
			}
			// Re-key ONLY a string-kinded key. Every other kind is compared by
			// identity or rebuilt wholesale, so a cleaned key is an object the
			// caller never had: a pointer key whose pointee held a dirty string
			// was reallocated, and the entry became unreachable — not by the
			// original key, and not by a cleaned one either, because the caller
			// cannot construct the new address.
			//
			// Nothing is given up by declining. toon-go accepts plain builtin
			// key types only, so a string key is the ONLY key that can reach
			// output at all. Measured through Check: map[string]string is OK,
			// while map[*T]string, map[any]string and a struct-keyed map are
			// each refused with "unsupported map key type". Re-keying a
			// non-string key therefore cannot enable output — it can only cost
			// the caller the lookup.
			//
			// The key is still WALKED above; only its rebuilt result is dropped.
			// That walk is what propagates an error from inside the key, which
			// TestSanitize_CollisionInMapKeyPropagates depends on.
			if keyChanged && iter.Key().Kind() != reflect.String {
				key = iter.Key()
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
		// Allocate on the FIRST changed field, exactly as the slice and array
		// branches do. This branch used to rebuild unconditionally and report
		// changed=true whatever its fields said, which made the returned flag a
		// lie: every ancestor pointer, interface, slice and array then allocated
		// a copy nothing below it needed. A struct whose fields all come back
		// unchanged is now passed through.
		t := v.Type()
		var out reflect.Value
		for i := 0; i < t.NumField(); i++ {
			if t.Field(i).PkgPath != "" {
				continue // unexported; unreachable by reflection
			}
			field, fieldChanged, err := sanitizeValue(v.Field(i))
			if err != nil {
				return reflect.Value{}, false, err
			}
			if !fieldChanged {
				continue
			}
			if !out.IsValid() {
				// Copy wholesale so unexported fields survive, then overwrite the
				// exported ones. reflect can set a whole struct value but not an
				// individual unexported field, which is why the order matters.
				out = reflect.New(v.Type()).Elem()
				out.Set(v)
			}
			// No CanSet guard: out came from reflect.New(...).Elem() so it is
			// addressable, and unexported fields were skipped above, so every
			// field reaching here is settable by construction.
			out.Field(i).Set(field)
		}
		if !out.IsValid() {
			return v, false, nil
		}
		return out, true, nil

	default:
		// Numbers, bools, funcs, channels: nothing to clean.
		return v, false, nil
	}
}

// asciiSafe maps ASCII bytes to whether they are guaranteed safe in TOON output
// (printable ASCII 0x20..0x7e, plus \t, \n, \r). Bytes >= 0x80 are non-ASCII and
// fall through to UTF-8 rune decoding.
var asciiSafe [256]bool

func init() {
	for b := 0; b < 256; b++ {
		if b < 0x80 {
			if b == '\n' || b == '\r' || b == '\t' || (b >= 0x20 && b <= 0x7e) {
				asciiSafe[b] = true
			}
		}
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
	// ASCII bytes are checked via asciiSafe without rune decoding. Non-ASCII
	// bytes fall through to utf8.DecodeRuneInString.
	i := 0
	for i < len(s) {
		b := s[i]
		if asciiSafe[b] {
			i++
			continue
		}
		if b < 0x80 {
			// Unsafe ASCII control rune or 0x7f (DEL)
			return cleanFrom(s, i)
		}
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
		c := s[i]
		if asciiSafe[c] {
			start := i
			for i < len(s) && asciiSafe[s[i]] {
				i++
			}
			b.WriteString(s[start:i])
			continue
		}
		if c < 0x80 {
			i++ // invalid/unsafe ASCII control byte: drop
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			i++ // invalid byte: drop it
			continue
		}
		if !unsafeRune(r) {
			b.WriteString(s[i : i+size])
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
