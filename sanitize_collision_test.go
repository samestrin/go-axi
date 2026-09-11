package goaxi

import (
	"errors"
	"strings"
	"testing"
)

// Adversarial finding (task 2 review), confirmed by this test before the fix:
// sanitizing map KEYS can make two distinct keys collide. "na\x1bme" and "name"
// both clean to "name", so one silently overwrote the other — and because Go
// randomizes map iteration order, WHICH one survived varied between runs.
//
// That is the exact failure AC1 forbids: a field disappears with no error. It is
// worse than an ordinary drop because it is nondeterministic, so a test that
// happened to pass once could fail later with no code change.
//
// Sanitize now refuses. Renaming a key would invent data the caller never wrote,
// and dropping one is the silent loss this module exists to prevent.
func TestSanitize_MapKeyCollisionIsRefused(t *testing.T) {
	in := map[string]any{
		"na\x1bme": "from-dirty-key",
		"name":     "from-clean-key",
	}

	// Run repeatedly: map iteration order is randomized, so a single run could
	// mask nondeterminism. The error must be raised every time regardless of
	// which key is visited first.
	for i := 0; i < 200; i++ {
		got, err := Sanitize(in)
		if err == nil {
			t.Fatalf("a cleaning-induced key collision must be refused, got %#v", got)
		}
		if got != nil {
			t.Errorf("no partial value may be returned alongside the error, got %#v", got)
		}

		var collision *KeyCollisionError
		if !errors.As(err, &collision) {
			t.Fatalf("error must be a *KeyCollisionError, got %T: %v", err, err)
		}
		if collision.Cleaned != "name" {
			t.Errorf("the error must name the cleaned key, got %#v", collision.Cleaned)
		}
		if collision.First == collision.Second {
			t.Errorf("the error must name both original keys, got %#v twice", collision.First)
		}
	}
}

// The error message has to make the invisible visible. A control byte does not
// render in a terminal, so an operator reading "keys name and name collide"
// would have no idea what to change. %#v quotes and escapes it.
func TestKeyCollisionError_MessageShowsTheInvisibleByte(t *testing.T) {
	err := &KeyCollisionError{Cleaned: "name", First: "name", Second: "na\x1bme"}
	msg := err.Error()

	if !strings.Contains(msg, `\x1b`) {
		t.Errorf("message must escape the control byte so it is readable, got: %s", msg)
	}
	if strings.ContainsRune(msg, '\x1b') {
		t.Errorf("message must not contain the raw control byte itself, got: %q", msg)
	}
}

// MustSanitize is for callers whose keys are fixed identifiers. It must panic on
// collision rather than return a value with a field missing.
func TestMustSanitize_PanicsOnCollision(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("MustSanitize must panic when a collision makes the result lossy")
		}
		if _, ok := r.(*KeyCollisionError); !ok {
			t.Errorf("panic value must be the *KeyCollisionError, got %T", r)
		}
	}()
	MustSanitize(map[string]any{"na\x1bme": 1, "name": 2})
}

func TestMustSanitize_ReturnsValueWhenClean(t *testing.T) {
	got, ok := MustSanitize(map[string]any{"a\x1bb": "x"}).(map[string]any)
	if !ok {
		t.Fatalf("map must stay a map[string]any, got %T", MustSanitize(map[string]any{}))
	}
	if got["ab"] != "x" {
		t.Errorf("key must be cleaned with its value intact, got %#v", got)
	}
}

// Propagation from the map KEY position specifically, not the value position.
//
// A map key must be comparable, which rules out using a map or slice directly
// as a key. A pointer is comparable regardless of what it points at, so a
// pointer key whose target holds a colliding map is the one construction that
// reaches the key branch. Contrived by design: the point is that the error
// escapes from the key path too, not only the value path.
func TestSanitize_CollisionInMapKeyPropagates(t *testing.T) {
	type holder struct {
		M map[string]any `toon:"m"`
	}
	key := &holder{M: map[string]any{"a\x1bb": 1, "ab": 2}}

	got, err := Sanitize(map[*holder]int{key: 7})
	if err == nil {
		t.Fatalf("a collision reached through a map key must propagate, got %#v", got)
	}
	var collision *KeyCollisionError
	if !errors.As(err, &collision) {
		t.Errorf("error must be a *KeyCollisionError, got %T: %v", err, err)
	}
}

// A collision between keys that are ALREADY clean cannot happen, so the ordinary
// path must be unaffected by the collision check.
func TestSanitize_DistinctCleanKeysAreUnaffected(t *testing.T) {
	in := map[string]any{"alpha": 1, "beta": 2, "gamma": 3}
	out, err := Sanitize(in)
	if err != nil {
		t.Fatalf("distinct clean keys must not error: %v", err)
	}
	got, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("map must stay a map[string]any, got %T", out)
	}
	if len(got) != 3 {
		t.Fatalf("distinct clean keys must all survive, got %#v", got)
	}
	for k, v := range in {
		if got[k] != v {
			t.Errorf("key %q must be unchanged, got %#v", k, got[k])
		}
	}
}

// A collision nested inside a slice or struct must propagate, not be swallowed
// by the branch that found it.
func TestSanitize_NestedCollisionPropagates(t *testing.T) {
	cases := []struct {
		name string
		in   any
	}{
		{"inside a slice", []any{map[string]any{"na\x1bme": 1, "name": 2}}},
		{"inside a map value", map[string]any{"outer": map[string]any{"a\x1bb": 1, "ab": 2}}},
		{"behind a pointer", &map[string]any{"a\x1bb": 1, "ab": 2}},
		{"inside an array", [1]any{map[string]any{"a\x1bb": 1, "ab": 2}}},
		{"inside a struct field", struct {
			M map[string]any `toon:"m"`
		}{M: map[string]any{"a\x1bb": 1, "ab": 2}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Sanitize(c.in)
			if err == nil {
				t.Fatalf("a nested collision must propagate, got %#v", got)
			}
			var collision *KeyCollisionError
			if !errors.As(err, &collision) {
				t.Errorf("error must be a *KeyCollisionError, got %T", err)
			}
		})
	}
}
