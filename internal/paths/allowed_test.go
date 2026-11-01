package paths

import "testing"

func TestAllowed(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"main.go", true},
		{"problems/sum/main.go", true},
		{"vendor/example.com/lib/x.go", true},
		{"go.mod", true},
		{"go.sum", true},
		{"vendor/modules.txt", true},
		{".gitignore", true},
		{"README.md", false},
		{"notes.txt", false},
		{"shot.png", false},
		{"../escape.go", false},
		{".", false},
	}
	for _, c := range cases {
		if got := Allowed(c.path); got != c.want {
			t.Errorf("Allowed(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}
