package main

import "testing"

func TestUSDFromEnv(t *testing.T) {
	const key = "PLOEG_ASK_BUDGET_USD"
	t.Setenv(key, "")
	if n, err := usdFromEnv(key, 0.02); err != nil || n != 0.02 {
		t.Errorf("unset = %v, %v; want the default", n, err)
	}
	for value, want := range map[string]float64{"0.0123": 0.0123, "2": 2, "10000": 10000} {
		t.Setenv(key, value)
		if n, err := usdFromEnv(key, 0.02); err != nil || n != want {
			t.Errorf("%q = %v, %v", value, n, err)
		}
	}
	for _, bad := range []string{"0", "-1", "0.00001", "10000.01", "NaN", "Inf", "two"} {
		t.Setenv(key, bad)
		if _, err := usdFromEnv(key, 0.02); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
