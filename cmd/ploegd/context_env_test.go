package main

import "testing"

func TestByteCountFromEnv(t *testing.T) {
	const key = "PLOEG_CONTEXT_MAX_BYTES"
	t.Setenv(key, "")
	if n, err := byteCountFromEnv(key, 7); err != nil || n != 7 {
		t.Errorf("unset = %d, %v; want the default", n, err)
	}
	t.Setenv(key, "1048576")
	if n, err := byteCountFromEnv(key, 7); err != nil || n != 1048576 {
		t.Errorf("set = %d, %v", n, err)
	}
	for _, bad := range []string{"0", "-1", "20MiB"} {
		t.Setenv(key, bad)
		if _, err := byteCountFromEnv(key, 7); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
