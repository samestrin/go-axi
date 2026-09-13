package goaxi

import (
	"fmt"
	"reflect"
	"strings"
)

// TagVerdict reports whether encoding a value as TOON would publish Go
// identifiers as its field names.
//
// OK is true when every encodable field carries a `toon` tag. Fields names the
// ones that do not, as "Type.Field", in the order they were found.
type TagVerdict struct {
	// OK is true when no field would be published under its Go identifier.
	OK bool

	// Fields names each field that would be, as "Type.Field".
	Fields []string
}

// String renders the verdict as a message suitable for a failing test or a
// build-time guard. It returns the empty string when the verdict is OK.
//
// Deliberately not named Error. A method of that name would make TagVerdict
// satisfy the error interface, so `var err error = verdict` would compile and
// never be nil — including for a passing verdict.
func (v TagVerdict) String() string {
	if v.OK {
		return ""
	}
	return fmt.Sprintf("%d field(s) would be published under their Go identifier: %s",
		len(v.Fields), strings.Join(v.Fields, ", "))
}

// CheckTags reports whether v's type is safe to hand to Format.Encode as TOON.
//
// WHY THIS EXISTS. toon-go does not fall back to the `json` struct tag. Probed
// against the pinned version: a struct carrying only `json` tags encodes to its
// Go identifiers, so a field whose json tag reads "count" still emits "Count: 3"
// rather than "count: 3". Nothing errors and nothing is empty, so neither Check
// nor a smoke test catches it. The output is simply keyed on names no consumer
// was told to expect, and a skill instructed that "the third column is SEVERITY"
// reads the wrong column.
//
// That is the same defect class Check was built for — a codec failing quietly
// rather than loudly — which is why the guard lives here rather than in each
// tool. Call it from a test over every type a command prints:
//
//	if v := goaxi.CheckTags(SearchResult{}); !v.OK {
//	    t.Error(v.String())
//	}
//
// A value that is already generic — a map, a slice, a scalar, or anything
// Format.EncodeProjected produced — carries no Go identifiers and is always OK.
// Unexported fields are never encoded, and a field marked `json:"-"` is never
// reached, so neither is flagged.
func CheckTags(v any) TagVerdict {
	verdict := TagVerdict{OK: true}
	if v == nil {
		return verdict
	}
	seen := make(map[reflect.Type]bool)
	walkTags(reflect.TypeOf(v), seen, &verdict)
	verdict.OK = len(verdict.Fields) == 0
	return verdict
}

// walkTags descends a type looking for encodable struct fields with no `toon`
// tag. It walks types rather than values so a nil pointer or an empty slice is
// still inspected, and it carries a seen set so a self-referential type
// terminates instead of recursing until the stack is gone.
func walkTags(t reflect.Type, seen map[reflect.Type]bool, verdict *TagVerdict) {
	if t == nil || seen[t] {
		return
	}
	seen[t] = true

	switch t.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array:
		walkTags(t.Elem(), seen, verdict)
	case reflect.Map:
		// Only the value side can carry a struct. toon-go accepts plain builtin
		// key types only, so a key is never a tagged struct.
		walkTags(t.Elem(), seen, verdict)
	case reflect.Struct:
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)

			// An unexported field is never encoded, so it cannot publish a name.
			// This is also what keeps time.Time and other opaque stdlib structs
			// from being reported: their fields are all unexported.
			if f.PkgPath != "" {
				continue
			}
			if name, _, _ := strings.Cut(f.Tag.Get("json"), ","); name == "-" {
				continue
			}
			if name, _, _ := strings.Cut(f.Tag.Get("toon"), ","); name == "" {
				verdict.Fields = append(verdict.Fields, t.Name()+"."+f.Name)
			}
			walkTags(f.Type, seen, verdict)
		}
	}
	// Every other kind — scalars, interfaces, funcs, channels — carries no field
	// names. An interface cannot be inspected statically: what it holds is known
	// only at run time, which is why a map[string]any comes back clean.
}
