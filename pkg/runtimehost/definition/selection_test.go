package definition

import (
	"reflect"
	"testing"
)

func TestGroupFlagsBaselineAndFeatureSelection(t *testing.T) {
	for _, baseline := range []string{"ci-baseline", "host-smoke"} {
		d := Defaults()
		d.BaselineTag = baseline
		tests := []TestSelection{{Tags: []string{baseline}}, {Tags: []string{"group-ownership"}}}
		for _, tt := range []struct {
			groups string
			want   []string
		}{
			{"", []string{}},
			{"baseline", []string{"--include-any-tag", baseline}},
			{"ownership", []string{"--include-any-tag", "group-ownership"}},
			{"baseline,ownership,baseline", []string{"--include-any-tag", baseline + ",group-ownership"}},
		} {
			got, err := d.GroupFlags(tt.groups, tests, nil)
			if err != nil || !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("baseline=%s groups=%q: got %v, %v; want %v", baseline, tt.groups, got, err, tt.want)
			}
		}
		for _, groups := range []string{"unknown", "empty", "baseline,unknown"} {
			if _, err := d.GroupFlags(groups, tests, map[string]bool{"empty": true}); err == nil {
				t.Fatalf("accepted unavailable selection %q", groups)
			}
		}
		if _, err := d.GroupFlags("baseline", tests[1:], nil); err == nil {
			t.Fatal("accepted baseline without a baseline scenario")
		}
	}
	got, err := GroupFlags("baseline", []TestSelection{{Tags: []string{"ci-baseline"}}}, nil)
	if err != nil || !reflect.DeepEqual(got, []string{"--include-any-tag", "ci-baseline"}) {
		t.Fatalf("default API: %v, %v", got, err)
	}
}
