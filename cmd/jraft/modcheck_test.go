package main

import "testing"

func TestFitsMinecraft(t *testing.T) {
	cases := []struct {
		deps []string
		ok   bool
	}{
		{nil, true},
		{[]string{"*"}, true},
		{[]string{">=26.3"}, true},
		{[]string{"~26.3"}, true},
		{[]string{">=26.1 <26.4"}, true},
		{[]string{"26.x"}, true},
		{[]string{"~1.21.11"}, false},
		{[]string{">=1.21.9 <=1.21.11"}, false},
		{[]string{"1.20.1"}, false},
		{[]string{"1.21.11", "26.3"}, true},
	}
	for _, c := range cases {
		if got := fitsMinecraft(c.deps, "26.3"); got != c.ok {
			t.Errorf("%v: got %v, want %v", c.deps, got, c.ok)
		}
	}
}
