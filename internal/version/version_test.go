package version

import "testing"

func TestCompare(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int
	}{
		{"v26.10.01.001", "v26.10.01.002", -1},
		{"26.10.02.001", "v26.10.01.999", 1},
		{"v26.10.01.001", "26.10.01.001", 0},
		{"v00.02.29.001", "v00.02.28.999", 1},
	} {
		got, err := Compare(tc.a, tc.b)
		if err != nil || got != tc.want {
			t.Errorf("Compare(%q, %q) = %d, %v", tc.a, tc.b, got, err)
		}
	}
	for _, v := range []string{"dev", "v26.02.29.001", "v26.13.01.001", "v26.10.00.001", "v26.10.01.000", "v26.10.01.1000", "vv26.10.01.001", "26.1.01.001", "v26.10.01.a01"} {
		if _, err := Compare(v, "v26.10.01.001"); err == nil {
			t.Errorf("accepted %q", v)
		}
		if _, err := Compare("v26.10.01.001", v); err == nil {
			t.Errorf("accepted rhs %q", v)
		}
	}
}
