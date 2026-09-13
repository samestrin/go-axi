package goaxi

import (
	"strings"
	"testing"
	"time"
)

func TestCheckTags_AcceptsDualTaggedType(t *testing.T) {
	v := CheckTags(dualTagged{})
	if !v.OK {
		t.Errorf("dual-tagged type reported as unsafe: %v", v.Fields)
	}
	if len(v.Fields) != 0 {
		t.Errorf("want no flagged fields, got %v", v.Fields)
	}
}

func TestCheckTags_FlagsFieldMissingAToonTag(t *testing.T) {
	// This is the silent defect the guard exists for. A field with only a `json`
	// tag encodes to its Go identifier, so a skill told "the third column is
	// SEVERITY" reads the wrong thing and nothing errors.
	v := CheckTags(jsonTagged{})
	if v.OK {
		t.Fatal("a type with no toon tags must not be reported OK")
	}
	joined := strings.Join(v.Fields, ",")
	for _, want := range []string{"Count", "File"} {
		if !strings.Contains(joined, want) {
			t.Errorf("field %q not flagged; got %v", want, v.Fields)
		}
	}
}

func TestCheckTags_FlagsCompletelyUntaggedField(t *testing.T) {
	type untagged struct {
		Name string
	}
	v := CheckTags(untagged{})
	if v.OK {
		t.Fatal("an untagged exported field must be flagged")
	}
}

func TestCheckTags_ReportsAPartiallyTaggedType(t *testing.T) {
	// The realistic failure: someone adds a field and forgets the second tag.
	type partial struct {
		Count int    `json:"count" toon:"count"`
		File  string `json:"file"`
	}
	v := CheckTags(partial{})
	if v.OK {
		t.Fatal("a partially tagged type must not be reported OK")
	}
	joined := strings.Join(v.Fields, ",")
	if !strings.Contains(joined, "File") {
		t.Errorf("the untagged field was not named; got %v", v.Fields)
	}
	if strings.Contains(joined, "Count") {
		t.Errorf("a correctly tagged field was flagged; got %v", v.Fields)
	}
}

func TestCheckTags_WalksNestedStructs(t *testing.T) {
	type inner struct {
		Line int `json:"line"`
	}
	type outer struct {
		Count int   `json:"count" toon:"count"`
		Inner inner `json:"inner" toon:"inner"`
	}
	v := CheckTags(outer{})
	if v.OK {
		t.Fatal("a nested type with an untagged field must be flagged")
	}
	if !strings.Contains(strings.Join(v.Fields, ","), "Line") {
		t.Errorf("nested field not reported; got %v", v.Fields)
	}
}

func TestCheckTags_WalksSlicesOfStructs(t *testing.T) {
	type row struct {
		Match string `json:"match"`
	}
	type result struct {
		Rows []row `json:"rows" toon:"rows"`
	}
	v := CheckTags(result{})
	if v.OK {
		t.Fatal("a struct reached through a slice must be walked")
	}
	if !strings.Contains(strings.Join(v.Fields, ","), "Match") {
		t.Errorf("field inside a slice element not reported; got %v", v.Fields)
	}
}

func TestCheckTags_WalksPointersAndMaps(t *testing.T) {
	type leaf struct {
		Depth int `json:"depth"`
	}
	type holder struct {
		Ptr *leaf           `json:"ptr"  toon:"ptr"`
		Map map[string]leaf `json:"map"  toon:"map"`
	}
	v := CheckTags(holder{})
	if v.OK {
		t.Fatal("structs behind a pointer or map value must be walked")
	}
}

func TestCheckTags_IgnoresUnexportedFields(t *testing.T) {
	type withPrivate struct {
		Count  int `json:"count" toon:"count"`
		hidden string
	}
	v := CheckTags(withPrivate{})
	if !v.OK {
		t.Errorf("unexported fields are never encoded and must not be flagged: %v", v.Fields)
	}
}

func TestCheckTags_IgnoresJSONSkippedFields(t *testing.T) {
	// A field marked `json:"-"` is never encoded, so it cannot publish a name.
	type skipped struct {
		Count int    `json:"count" toon:"count"`
		Junk  string `json:"-"`
	}
	v := CheckTags(skipped{})
	if !v.OK {
		t.Errorf("a json:\"-\" field must not be flagged: %v", v.Fields)
	}
}

func TestCheckTags_OKOnScalarAndMapPayloads(t *testing.T) {
	// A payload that is already generic carries no Go identifiers to leak. This
	// is exactly what EncodeProjected produces, so it must come back clean.
	for _, v := range []any{
		map[string]any{"count": 3},
		[]any{1, 2, 3},
		"a string",
		42,
		nil,
	} {
		if got := CheckTags(v); !got.OK {
			t.Errorf("CheckTags(%#v) flagged %v; a generic payload has no tags to miss", v, got.Fields)
		}
	}
}

func TestCheckTags_SurvivesARecursiveType(t *testing.T) {
	// A self-referential type must terminate rather than recurse forever.
	type node struct {
		Name string `json:"name" toon:"name"`
		Next *node  `json:"next" toon:"next"`
	}
	done := make(chan TagVerdict, 1)
	go func() { done <- CheckTags(node{}) }()
	select {
	case v := <-done:
		if !v.OK {
			t.Errorf("fully tagged recursive type flagged: %v", v.Fields)
		}
	case <-time.After(time.Second):
		t.Fatal("CheckTags did not terminate on a recursive type")
	}
}
