package instance_anomaly

import (
	"testing"

	"github.com/google/uuid"
)

// The dedupe key is what keeps this table readable. A refused device retries
// every second or two, so "same condition → same key" is not a nicety: without
// it an operator opens the console to thousands of identical rows.
func TestRepeatsOfTheSameConflictShareAKey(t *testing.T) {
	requesting, owner := uuid.New(), uuid.New()

	first := SerialConflictKey("10000000abcd0005", requesting, owner)
	second := SerialConflictKey("10000000abcd0005", requesting, owner)

	if first != second {
		t.Error("the same conflict must collapse onto one row")
	}
}

// Equally important in the other direction: two genuinely different problems
// must not be folded into one, or acknowledging one would silence the other.
func TestDistinctConflictsGetDistinctKeys(t *testing.T) {
	requesting, owner, other := uuid.New(), uuid.New(), uuid.New()
	base := SerialConflictKey("10000000abcd0005", requesting, owner)

	for name, key := range map[string]string{
		"another serial":             SerialConflictKey("10000000abcd0006", requesting, owner),
		"another requesting account": SerialConflictKey("10000000abcd0005", other, owner),
		"another owning account":     SerialConflictKey("10000000abcd0005", requesting, other),
	} {
		if key == base {
			t.Errorf("%s must be a separate anomaly", name)
		}
	}
}

// The serial identifies a customer's physical hardware. It belongs in Details
// for whoever is investigating, not in an index every list query reads.
func TestKeyDoesNotLeakTheSerial(t *testing.T) {
	key := SerialConflictKey("10000000abcd0005", uuid.New(), uuid.New())

	if len(key) != 64 {
		t.Fatalf("expected a sha256 digest, got %d chars", len(key))
	}
	if key == "10000000abcd0005" || contains(key, "10000000abcd") {
		t.Error("the raw serial must not appear in the dedupe key")
	}
}

// A host condition is one ongoing problem, not one per measurement.
func TestHostConditionsCollapseToOneRowPerCondition(t *testing.T) {
	if HostConditionKey(KindHostDiskLow) != HostConditionKey(KindHostDiskLow) {
		t.Error("repeated measurements of one condition must share a row")
	}
	if HostConditionKey(KindHostDiskLow) == HostConditionKey(KindHostCPULoad) {
		t.Error("different host conditions must be separate anomalies")
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
