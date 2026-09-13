package goaxi

import (
	"errors"
	"testing"
)

// keyBox is a pointer-keyed map's key type: a struct reached only through its
// address, carrying a string that needs cleaning.
type keyBox struct{ Label string }

// A map key the caller holds by POINTER must still resolve after sanitizing.
//
// The map branch discarded the key's changed flag, so a pointer key whose pointee
// held a dirty string took the Pointer branch, which allocates a FRESH pointer.
// The result was keyed on an address the caller has never seen and cannot
// construct, so the entry was unreachable by any means: not by the original key,
// not by a cleaned one. Probed before the fix, the caller held 0x…840 and the
// result was keyed 0x…950. That is silent loss wearing a different hat.
//
// Contrast a dirty STRING key, where re-keying is intended and survivable: the
// caller can look the entry up under the cleaned key, and two keys cleaning to
// one are refused outright as a *KeyCollisionError. Neither escape hatch exists
// for a pointer.
func TestSanitize_PointerMapKeyStaysTheCallersKey(t *testing.T) {
	k := &keyBox{Label: "dirty\x1bvalue"}
	in := map[*keyBox]string{k: "payload"}

	got, err := Sanitize(in)
	if err != nil {
		t.Fatalf("sanitizing a pointer-keyed map must not error: %v", err)
	}

	out, ok := got.(map[*keyBox]string)
	if !ok {
		t.Fatalf("result must keep the input's map type, got %T", got)
	}
	if len(out) != 1 {
		t.Fatalf("result must keep the single entry, got %d entries", len(out))
	}

	v, found := out[k]
	if !found {
		t.Fatalf("the caller's own key no longer resolves: the map was re-keyed to a "+
			"fresh pointer the caller cannot construct, so the entry is unreachable. "+
			"result = %#v", out)
	}
	if v != "payload" {
		t.Errorf("value under the caller's key = %q, want %q", v, "payload")
	}
}

// An interface-kinded key is the same defect wearing one more layer: the static
// key type is `interface{}`, so the Interface branch rebuilds it and the dynamic
// pointer underneath is reallocated. Kind() is Interface rather than Pointer, so
// a fix keyed on Pointer alone would miss this.
func TestSanitize_InterfaceMapKeyStaysTheCallersKey(t *testing.T) {
	k := &keyBox{Label: "dirty\x1bvalue"}
	in := map[any]string{k: "payload"}

	got, err := Sanitize(in)
	if err != nil {
		t.Fatalf("sanitizing an interface-keyed map must not error: %v", err)
	}

	out, ok := got.(map[any]string)
	if !ok {
		t.Fatalf("result must keep the input's map type, got %T", got)
	}
	if _, found := out[k]; !found {
		t.Errorf("the caller's own key no longer resolves after re-keying, result = %#v", out)
	}
}

// The guard against over-correcting. Refusing to re-key must apply ONLY to the
// kinds whose equality is by identity — it must not stop cleaning ordinary string
// keys, which is the behaviour the module exists to provide.
//
// A string key is also the only key toon-go accepts. Measured through Check:
// map[string]string is OK, while map[*keyBox]string, map[any]string and a struct
// key are each refused with "unsupported map key type … toon-go supports plain
// builtin types only". That asymmetry is the whole justification for the rule:
// re-keying a non-string key can never enable output, so it can only cost the
// caller their lookup.
func TestSanitize_StringMapKeyIsStillCleaned(t *testing.T) {
	in := map[string]any{"na\x1bme": "payload"}

	got, err := Sanitize(in)
	if err != nil {
		t.Fatalf("Sanitize: %v", err)
	}
	out, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("result must stay a map[string]any, got %T", got)
	}
	if v, found := out["name"]; !found || v != "payload" {
		t.Errorf("a dirty string key must still be cleaned to %q, got %#v", "name", out)
	}
	if _, found := out["na\x1bme"]; found {
		t.Errorf("the dirty string key must not survive in the result, got %#v", out)
	}
}

// Preserving the caller's key must not stop the key from being WALKED. The key is
// still sanitized — only its re-keyed result is discarded — because that walk is
// what surfaces errors from inside the pointee. TestSanitize_CollisionInMapKeyPropagates
// covers the collision case; this pins the narrower rule it depends on, so a
// future "optimization" that skips the walk for non-string keys fails here too.
func TestSanitize_PointerMapKeyIsStillWalkedForErrors(t *testing.T) {
	type holder struct {
		M map[string]any `toon:"m"`
	}
	key := &holder{M: map[string]any{"a\x1bb": 1, "ab": 2}}

	got, err := Sanitize(map[*holder]int{key: 7})
	if err == nil {
		t.Fatalf("an error from inside a preserved key must still propagate, got %#v", got)
	}
	var collision *KeyCollisionError
	if !errors.As(err, &collision) {
		t.Errorf("error must be a *KeyCollisionError, got %T: %v", err, err)
	}
}
