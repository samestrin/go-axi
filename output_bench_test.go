package goaxi

import (
	"io"
	"testing"
)

// The cost of each way of writing a payload, kept runnable rather than quoted
// from a commit message that goes stale.
//
//	go test -run '^$' -bench Output -benchmem
//
// Read it as three questions:
//
//   - Encode is the floor: sanitize, then marshal, no guard at all.
//   - EncodeChecked and EncodeOrJSON add a guard. Their gap above Encode is one
//     lossy walk over the payload, about 6 allocations per row — the same
//     reflect map-iteration boxing every walk in this package pays. Closing that
//     gap means merging the cycle walk and the lossy walk into a single pass,
//     which has NOT been done.
//   - Check_then_Encode is the anti-pattern the checked writers exist to
//     replace. It sanitizes twice and marshals twice to serve one guard.
func BenchmarkOutput_Writers(b *testing.B) {
	v := losslessRows(2000)

	b.Run("Encode", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if err := Encode(io.Discard, v); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("EncodeChecked", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, err := EncodeChecked(io.Discard, v); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("EncodeOrJSON", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if err := EncodeOrJSON(io.Discard, v); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("Check_then_Encode", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if got := Check(v); !got.OK {
				b.Fatal(got.Reason)
			}
			if err := Encode(io.Discard, v); err != nil {
				b.Fatal(err)
			}
		}
	})
}

// The same three writers on a payload that needs cleaning, so the cost of the
// rebuild is visible beside the cost of the guard rather than hidden by it.
func BenchmarkOutput_WritersDirty(b *testing.B) {
	v := benchRows(2000, true)

	b.Run("Encode", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if err := Encode(io.Discard, v); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("EncodeOrJSON", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if err := EncodeOrJSON(io.Discard, v); err != nil {
				b.Fatal(err)
			}
		}
	})
}
