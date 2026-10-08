package ownedkind

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFingerprintIsPortableAndDetectsSourceAndPlanDrift(t *testing.T) {
	makeInput := func() (string, []Image) {
		root := t.TempDir()
		if err := os.Mkdir(filepath.Join(root, "fixtures"), 0700); err != nil {
			t.Fatal(err)
		}
		for file, data := range map[string]string{"fixtures/Dockerfile": "FROM scratch\n", "fixtures/marker": "proof"} {
			if err := os.WriteFile(filepath.Join(root, file), []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
		}
		return root, []Image{{Alias: "proof:kind", Build: &Build{Context: filepath.Join(root, "fixtures"), Dockerfile: filepath.Join(root, "fixtures/Dockerfile")}}}
	}
	first, images := makeInput()
	second, other := makeInput()
	id, err := Fingerprint(first, images, []string{"fixtures"})
	if err != nil {
		t.Fatal(err)
	}
	same, err := Fingerprint(second, other, []string{"fixtures"})
	if err != nil || id != same {
		t.Fatal("checkout paths affected fingerprint", err)
	}
	if err := os.WriteFile(filepath.Join(first, "fixtures/marker"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	changed, err := Fingerprint(first, images, []string{"fixtures"})
	if err != nil || changed == id {
		t.Fatal("source drift ignored")
	}
	images[0].Alias = "different:kind"
	different, err := Fingerprint(first, images, []string{"fixtures"})
	if err != nil || different == changed {
		t.Fatal("plan drift ignored")
	}
	if err := os.Symlink(filepath.Join(first, "fixtures/marker"), filepath.Join(first, "fixtures/link")); err != nil {
		t.Fatal(err)
	}
	if _, err := Fingerprint(first, images, []string{"fixtures"}); err == nil {
		t.Fatal("linked source accepted")
	}
	if _, err := Fingerprint(first, images, []string{"../outside"}); err == nil {
		t.Fatal("outside input accepted")
	}
}
