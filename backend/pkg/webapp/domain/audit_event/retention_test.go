package audit_event

import "testing"

func TestEffectiveRetentionDays(t *testing.T) {
	ptr := func(v int) *int { return &v }
	cases := []struct {
		name    string
		account *int
		install int
		want    int
	}{
		{"no account value: the install default", nil, 365, 365},
		{"install keeps forever", nil, 0, 0},
		{"account keeps longer than the install", ptr(1000), 30, 1000},
		{"account keeps shorter than the install", ptr(30), 365, 30},
		{"account keeps forever on a bounded install", ptr(0), 30, 0},
		{"account value below the floor is raised", ptr(1), 365, MinRetentionDays},
		{"install default below the floor is raised", nil, 2, MinRetentionDays},
		{"account value above the ceiling is lowered", ptr(100000), 365, MaxRetentionDays},
		{"negative account value falls back to the install", ptr(-5), 90, 90},
		{"negative install default keeps forever", nil, -1, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := EffectiveRetentionDays(tc.account, tc.install); got != tc.want {
				t.Errorf("EffectiveRetentionDays = %d, want %d", got, tc.want)
			}
		})
	}
}
