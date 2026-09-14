package goaxi

// decodeRowsFast is the Tier 2 fast path: it tries to build rows directly
// from the already-collected row text, skipping toon-go's generic decoder
// (synthesize + toon.DecodeString + arrayOf + projectValue) entirely.
//
// It returns ok == false whenever it cannot PROVE its output would match
// decodeRowsGeneric exactly, and DecodeTabular falls back to that proven
// path in that case. Stub: always declines, so this compiles with no
// behavior change until the real eligibility scan and token decoder land.
func decodeRowsFast(h *header, rows []string) ([]map[string]string, bool) {
	return nil, false
}
