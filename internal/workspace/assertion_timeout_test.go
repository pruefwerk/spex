package workspace

import "testing"

func TestProbeAssertionTimeoutUsesJobDeadline(t *testing.T) {
	for _, test := range []struct {
		job  string
		want int
	}{
		{"spec:\n  activeDeadlineSeconds: 50\n", 65},
		{"      spec:\n        activeDeadlineSeconds: 90\n", 105},
		{"spec:\n  activeDeadlineSeconds: 630\n", 645},
		{"spec:\n  activeDeadlineSeconds: invalid\n", 120},
		{"", 120},
	} {
		if got := probeAssertionTimeout(test.job); got != test.want {
			t.Errorf("probeAssertionTimeout(%q) = %d; want %d", test.job, got, test.want)
		}
	}
}
