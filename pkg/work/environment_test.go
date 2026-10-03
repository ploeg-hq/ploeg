package work

import "testing"

func TestNormalizeEnvironment(t *testing.T) {
	for in, want := range map[string]string{
		"production":  "production",
		" Production": "production",
		"TEST":        "test",
		"eu-west.1_a": "eu-west.1_a",
	} {
		if got, ok := NormalizeEnvironment(in); !ok || got != want {
			t.Errorf("NormalizeEnvironment(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"", " ", "-prod", "prod env", "prod/eu", "ü", string(make([]byte, 64))} {
		if _, ok := NormalizeEnvironment(in); ok {
			t.Errorf("NormalizeEnvironment(%q) accepted", in)
		}
	}
	long := "a"
	for len(long) < 64 {
		long += "a"
	}
	if _, ok := NormalizeEnvironment(long[:63]); !ok {
		t.Error("63 characters must be accepted")
	}
	if _, ok := NormalizeEnvironment(long); ok {
		t.Error("64 characters must be refused")
	}
}
