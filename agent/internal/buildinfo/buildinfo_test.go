package buildinfo

import "testing"

func TestCompareStableTags(t *testing.T) {
	tests := []struct {
		name string
		a    string
		b    string
		want int
	}{
		{name: "numeric minor ordering", a: "v1.9.0", b: "v1.10.0", want: -1},
		{name: "major ordering", a: "v2.0.0", b: "v1.99.99", want: 1},
		{name: "equal", a: "v1.2.3", b: "v1.2.3", want: 0},
		{name: "large numeric components", a: "v999999999999999999999999.0.0", b: "v1000000000000000000000000.0.0", want: -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := CompareStableTags(tt.a, tt.b)
			if err != nil || got != tt.want {
				t.Fatalf("CompareStableTags(%q, %q) = (%d, %v), want (%d, nil)", tt.a, tt.b, got, err, tt.want)
			}
		})
	}
}

func TestCompareStableTagsRejectsInvalidTags(t *testing.T) {
	invalid := []string{
		"1.2.3",
		"v01.2.3",
		"v1.02.3",
		"v1.2.03",
		"v1.2.3-rc.1",
		"v1.2.3+build.1",
		"v1.x.3",
		"v1.2",
		"v1.2.3.4",
		" v1.2.3",
	}
	for _, tag := range invalid {
		t.Run(tag, func(t *testing.T) {
			if got, err := CompareStableTags(tag, "v1.2.3"); err == nil {
				t.Fatalf("CompareStableTags(%q, %q) = (%d, nil), want an error", tag, "v1.2.3", got)
			}
		})
	}
}

func TestCompareStableTagsRejectsInvalidSecondTag(t *testing.T) {
	if got, err := CompareStableTags("v1.2.3", "v1.2.03"); err == nil {
		t.Fatalf("CompareStableTags() = (%d, nil), want an error for second tag", got)
	}
}

func TestDisplay(t *testing.T) {
	originalVersion, originalRepository := Version, Repository
	t.Cleanup(func() {
		Version, Repository = originalVersion, originalRepository
	})

	tests := []struct {
		name       string
		version    string
		repository string
		want       string
	}{
		{name: "tagged release", version: "v1.2.3", repository: "owner/repo", want: "cf-san v1.2.3 (owner/repo)"},
		{name: "development build", version: "dev", repository: "", want: "cf-san development build"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			Version, Repository = tt.version, tt.repository
			if got := Display(); got != tt.want {
				t.Fatalf("Display() = %q, want %q", got, tt.want)
			}
		})
	}
}
