package lab

import "testing"

func TestKeyedEntropyDoesNotShiftExistingSamples(t *testing.T) {
	entropy, err := NewEntropy("seed:policy-comparison")
	if err != nil {
		t.Fatalf("open entropy: %v", err)
	}

	before, err := entropy.Uint64("actual-runtime/run-a")
	if err != nil {
		t.Fatalf("draw first sample: %v", err)
	}
	if _, err := entropy.Uint64("unrelated/new-sample"); err != nil {
		t.Fatalf("draw unrelated sample: %v", err)
	}
	after, err := entropy.Uint64("actual-runtime/run-a")
	if err != nil {
		t.Fatalf("draw repeated sample: %v", err)
	}

	if after != before {
		t.Fatalf("sample changed from %d to %d after unrelated draw", before, after)
	}
}

func TestKeyedEntropyRejectsAnEmptySemanticKey(t *testing.T) {
	entropy, err := NewEntropy("seed:policy-comparison")
	if err != nil {
		t.Fatalf("open entropy: %v", err)
	}

	if _, err := entropy.Uint64(""); err == nil {
		t.Fatal("entropy accepted an empty semantic key")
	}
}

func TestDeterministicIDUsesSemanticIdentity(t *testing.T) {
	first := DeterministicID("seed:ids", "event", "arrival/run-a")
	repeated := DeterministicID("seed:ids", "event", "arrival/run-a")
	other := DeterministicID("seed:ids", "event", "arrival/run-b")

	if first != repeated {
		t.Fatalf("repeated id = %q, want %q", repeated, first)
	}
	if first == other {
		t.Fatalf("different semantic identities produced %q", first)
	}
}
