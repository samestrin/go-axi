package goaxi

import (
	"reflect"
	"strings"
	"unicode"
	"unicode/utf8"
)

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
func Sanitize(v any) any {
	if v == nil {
		return nil
	}
	return sanitizeValue(reflect.ValueOf(v)).Interface()
}

// SanitizeString cleans a single string using the same rules as Sanitize. It is
// exported for callers that build output a field at a time rather than handing
// over a whole value.
func SanitizeString(s string) string {
	return cleanString(s)
}

// sanitizeValue walks v and returns a cleaned copy of the same type.
//
// Every branch builds a new value rather than mutating in place. The input may
// be shared with the caller, and a sanitizer with a side effect on its argument
// is a trap: a caller that later writes the same value as JSON would silently
// get the stripped version.
func sanitizeValue(v reflect.Value) reflect.Value {
	switch v.Kind() {
	case reflect.String:
		out := reflect.New(v.Type()).Elem()
		out.SetString(cleanString(v.String()))
		return out

	case reflect.Interface:
		if v.IsNil() {
			return v
		}
		out := reflect.New(v.Type()).Elem()
		out.Set(sanitizeValue(v.Elem()))
		return out

	case reflect.Pointer:
		if v.IsNil() {
			return v
		}
		out := reflect.New(v.Type().Elem())
		out.Elem().Set(sanitizeValue(v.Elem()))
		return out

	case reflect.Slice:
		if v.IsNil() {
			return v
		}
		out := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		for i := 0; i < v.Len(); i++ {
			out.Index(i).Set(sanitizeValue(v.Index(i)))
		}
		return out

	case reflect.Array:
		out := reflect.New(v.Type()).Elem()
		for i := 0; i < v.Len(); i++ {
			out.Index(i).Set(sanitizeValue(v.Index(i)))
		}
		return out

	case reflect.Map:
		if v.IsNil() {
			return v
		}
		// Keys are sanitized too. A key is a field name in tabular output, so a
		// control byte there lands in the header rather than a cell.
		out := reflect.MakeMapWithSize(v.Type(), v.Len())
		iter := v.MapRange()
		for iter.Next() {
			out.SetMapIndex(sanitizeValue(iter.Key()), sanitizeValue(iter.Value()))
		}
		return out

	case reflect.Struct:
		// Copy wholesale first so unexported fields survive, then overwrite the
		// exported ones. reflect can set a whole struct value but not an
		// individual unexported field, which is why the order matters.
		out := reflect.New(v.Type()).Elem()
		out.Set(v)
		t := v.Type()
		for i := 0; i < t.NumField(); i++ {
			if t.Field(i).PkgPath != "" {
				continue // unexported; unreachable by reflection
			}
			if f := out.Field(i); f.CanSet() {
				f.Set(sanitizeValue(v.Field(i)))
			}
		}
		return out

	default:
		// Numbers, bools, funcs, channels: nothing to clean.
		return v
	}
}

// cleanString drops unsafe runes and invalid UTF-8 bytes.
//
// The scan is manual rather than a range loop because range cannot distinguish a
// genuine U+FFFD in the input from an invalid byte decoded as one. A lone 0x9b
// must be dropped; a real replacement character the caller supplied must not be.
func cleanString(s string) string {
	if isClean(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
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

// isClean reports whether s needs no changes, so the common case returns the
// original string without allocating.
func isClean(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unsafeRune(r) {
			return false
		}
	}
	return true
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
