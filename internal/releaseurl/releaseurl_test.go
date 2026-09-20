package releaseurl

import "testing"

func TestNormalizeAccelerator(t *testing.T) {
	tests := map[string]string{
		"":                         "",
		" https://ghfast.top ":     "https://ghfast.top/",
		"https://mirror.test/base": "https://mirror.test/base/",
	}
	for input, want := range tests {
		got, err := NormalizeAccelerator(input)
		if err != nil || got != want {
			t.Fatalf("NormalizeAccelerator(%q)=%q, %v; want %q", input, got, err, want)
		}
	}
	for _, input := range []string{"http://ghfast.top", "javascript://x", "file:///tmp/x", "https://user@example.com/", "https://example.com/?x=1"} {
		if _, err := NormalizeAccelerator(input); err == nil {
			t.Fatalf("unsafe accelerator %q was accepted", input)
		}
	}
}

func TestCandidatesOnlyAcceleratesReleaseAssetsAndFallsBack(t *testing.T) {
	original := "https://github.com/ImoLR/mmwx-custom/releases/download/v1.4.1/mmwxc-helper-linux-amd64"
	got, err := Candidates("https://ghfast.top/", original)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "https://ghfast.top/"+original || got[1] != original {
		t.Fatalf("unexpected candidates: %#v", got)
	}
	without, err := Candidates("", original)
	if err != nil || len(without) != 1 || without[0] != original {
		t.Fatalf("disabled accelerator candidates=%#v err=%v", without, err)
	}
	for _, unsafe := range []string{
		"https://api.github.com/repos/ImoLR/mmwx-custom/releases/latest",
		"https://github.com/ImoLR/mmwx-custom",
		"https://example.com/ImoLR/mmwx-custom/releases/download/v1/a",
	} {
		if _, err := Candidates(DefaultAccelerator, unsafe); err == nil {
			t.Fatalf("non-Release URL %q was accelerated", unsafe)
		}
	}
}
