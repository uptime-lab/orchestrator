package schema

import (
	"errors"
	"strings"
	"testing"
)

func TestDecide(t *testing.T) {
	cases := []struct {
		name   string
		ledger Ledger
		build  int
		want   Decision
	}{
		{"table absent (fresh or pre-ledger database)", Ledger{Present: false}, 3, Migrate},
		{"table absent ignores a stale Highest", Ledger{Present: false, Highest: 9}, 3, Migrate},
		{"table present but empty", Ledger{Present: true, Highest: 0}, 1, Migrate},
		{"older schema", Ledger{Present: true, Highest: 2}, 3, Migrate},
		{"same schema", Ledger{Present: true, Highest: 3, Complete: true}, 3, Current},
		{"same schema, its migration unfinished", Ledger{Present: true, Highest: 3}, 3, Migrate},
		{"newer schema", Ledger{Present: true, Highest: 4, Complete: true}, 3, Refuse},
		{"newer schema whose migration failed part-way", Ledger{Present: true, Highest: 4}, 3, Refuse},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Decide(tc.ledger, tc.build); got != tc.want {
				t.Fatalf("Decide(%+v, %d) = %v, want %v", tc.ledger, tc.build, got, tc.want)
			}
		})
	}
}

func TestCheckRefusalNamesBothVersionsAndThatNothingWasTouched(t *testing.T) {
	d, err := Check(Ledger{Present: true, Highest: 7}, 5)
	if d != Refuse {
		t.Fatalf("decision = %v, want refuse", d)
	}
	var newer *NewerSchemaError
	if !errors.As(err, &newer) {
		t.Fatalf("err = %v, want *NewerSchemaError", err)
	}
	if newer.Stored != 7 || newer.Build != 5 {
		t.Fatalf("versions = %d/%d, want 7/5", newer.Stored, newer.Build)
	}
	msg := err.Error()
	for _, want := range []string{"version 7", "version 5", "NOT touched", "restore"} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal %q does not say %q", msg, want)
		}
	}
}

func TestCheckAllowsEverythingButNewer(t *testing.T) {
	for _, l := range []Ledger{{}, {Present: true}, {Present: true, Highest: Version - 1}, {Present: true, Highest: Version}} {
		if _, err := Check(l, Version); err != nil {
			t.Errorf("Check(%+v) refused: %v", l, err)
		}
	}
}

func TestVersionStartsAtOne(t *testing.T) {
	// 0 is what an empty ledger reads as; a build at 0 would never be
	// distinguishable from "nothing recorded".
	if Version < 1 {
		t.Fatalf("Version = %d, must be at least 1", Version)
	}
}
