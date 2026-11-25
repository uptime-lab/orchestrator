package agentpkg

import (
	"reflect"
	"testing"
)

func TestVersionsSortsNumericallyAndPutsLatestFirst(t *testing.T) {
	got := Versions([]string{
		"orchestrator-agent_0.2.9_linux_arm64.deb",
		"orchestrator-agent_0.2.14_linux_arm64.deb",
		"orchestrator-agent_latest_linux_arm64.deb",
		"orchestrator-agent_0.4.7_linux_arm64.deb",
		"SHA256SUMS",
		"orchestrator-agent_0.4.7_linux_arm64.deb.sig",
	})
	want := []string{"latest", "0.4.7", "0.2.14", "0.2.9"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v — a lexicographic sort ranks 0.2.9 above 0.2.14", got, want)
	}
}

func TestVersionsDeduplicatesAcrossArchesAndFormats(t *testing.T) {
	got := Versions([]string{
		"orchestrator-agent_0.4.7_linux_arm64.deb",
		"orchestrator-agent_0.4.7_linux_amd64.deb",
		"orchestrator-agent_0.4.7_linux_arm64.tar.gz",
	})
	if want := []string{"0.4.7"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestVersionsIgnoresForeignNames(t *testing.T) {
	if got := Versions([]string{"boot.img", "notes.txt", "orchestrator-agent.deb"}); len(got) != 0 {
		t.Fatalf("got %v, want none", got)
	}
}

func TestLess(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"0.2.9", "0.2.10", true},
		{"0.2.10", "0.2.9", false},
		{"0.4.7", "0.4.7", false},
		{"0.4", "0.4.1", true}, // a prefix sorts lower
		{"0.4.7", "latest", true},
	}
	for _, c := range cases {
		if got := Less(c.a, c.b); got != c.want {
			t.Errorf("Less(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}
