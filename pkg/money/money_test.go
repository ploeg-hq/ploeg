package money

import "testing"

func TestUSDFormatsNlNL(t *testing.T) {
	for in, want := range map[float64]string{
		0:      "US$ 0,00",
		0.06:   "US$ 0,06",
		0.005:  "US$ 0,01",
		12.345: "US$ 12,35",
		1234.5: "US$ 1.234,50",
		-3:     "US$ -3,00",
	} {
		if got := USD(in); got != want {
			t.Errorf("USD(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestCountGroupsThousands(t *testing.T) {
	for in, want := range map[int64]string{0: "0", 999: "999", 15004: "15.004", 1234567: "1.234.567", -15004: "-15.004"} {
		if got := Count(in); got != want {
			t.Errorf("Count(%d) = %q, want %q", in, got, want)
		}
	}
}
