package node

import (
	"testing"
	"time"
)

func TestAdoptionRefusalIsLive(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name string
		last time.Time
		want bool
	}{
		{"seconds ago", now.Add(-3 * time.Second), true},
		{"just inside the window", now.Add(-AdoptionRefusalLiveWindow + time.Second), true},
		{"at the window", now.Add(-AdoptionRefusalLiveWindow), false},
		{"an hour ago", now.Add(-time.Hour), false},
		{"zero time is never live", time.Time{}, false},
	}
	for _, c := range cases {
		if got := AdoptionRefusalIsLive(c.last, now); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}
