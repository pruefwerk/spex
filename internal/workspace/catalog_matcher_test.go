package workspace

import (
	"reflect"
	"sync"
	"testing"
)

func TestCatalogMatcherPreservesCaptureSemantics(t *testing.T) {
	for _, test := range []struct {
		expression, text string
		values           map[string]string
		match            bool
	}{
		{"literal [text]", "literal [text]", map[string]string{}, true},
		{"literal [text]", "literal text", nil, false},
		{"device {id} has {count:number} samples", "device abc has -2.5 samples", map[string]string{"id": "abc", "count": "-2.5"}, true},
		{"device {id} has {count:number} samples", "device abc has unknown samples", nil, false},
	} {
		for i := 0; i < 2; i++ {
			values, match, err := matchCatalogExpression(test.expression, test.text)
			if err != nil || match != test.match || !reflect.DeepEqual(values, test.values) {
				t.Fatal(values, match, err)
			}
		}
	}
}

func TestCatalogMatcherConcurrentReuse(t *testing.T) {
	var workers sync.WaitGroup
	for i := 0; i < 20; i++ {
		workers.Go(func() {
			for j := 0; j < 20; j++ {
				v, ok, err := matchCatalogExpression("device {id}", "device alpha")
				if err != nil || !ok || v["id"] != "alpha" {
					t.Error(v, ok, err)
				}
			}
		})
	}
	workers.Wait()
}
