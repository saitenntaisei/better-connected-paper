package store

import (
	"reflect"
	"testing"
)

func TestIsKnownBogusWorkID(t *testing.T) {
	cases := []struct {
		id   string
		want bool
	}{
		{"W4385245566", true},  // MizAR
		{"W4292779060", true},  // Aion Framework
		{"W4399695759", false}, // OpenVLA — legit
		{"W2163605009", false}, // AlexNet — legit (high cc, but real foundation)
		{"", false},
		{"unknown", false},
	}
	for _, c := range cases {
		if got := IsKnownBogusWorkID(c.id); got != c.want {
			t.Errorf("IsKnownBogusWorkID(%q) = %v, want %v", c.id, got, c.want)
		}
	}
}

func TestFilterBogusIDs(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"empty", nil, nil},
		{
			"no bogus — same slice returned",
			[]string{"W1", "W2", "W3"},
			[]string{"W1", "W2", "W3"},
		},
		{
			"single MizAR ref filtered",
			[]string{"W1", "W4385245566", "W2"},
			[]string{"W1", "W2"},
		},
		{
			"both known-bad targets filtered",
			[]string{"W4385245566", "W1", "W4292779060", "W2"},
			[]string{"W1", "W2"},
		},
		{
			"all bogus — empty result",
			[]string{"W4385245566", "W4292779060"},
			[]string{},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := filterBogusIDs(c.in)
			if len(got) == 0 && len(c.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("filterBogusIDs(%v) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

// filterBogusIDs MUST return the input slice unchanged (same backing
// array) when nothing needs filtering, so the hot path doesn't pay an
// allocation per ReplacePaperLinks call. Detect a regression by
// comparing the underlying pointer via cap-aware slice comparison.
func TestFilterBogusIDsNoAllocWhenClean(t *testing.T) {
	in := []string{"W1", "W2", "W3"}
	out := filterBogusIDs(in)
	if &in[0] != &out[0] {
		t.Errorf("filterBogusIDs allocated a new slice for clean input; want same backing array")
	}
}
