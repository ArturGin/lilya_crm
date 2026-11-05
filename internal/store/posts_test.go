package store

import "testing"

func TestBuildTSQuery(t *testing.T) {
	cases := map[string]string{
		"":                 "",
		"  ":               "",
		"голубь":           "голубь:*",
		"Hip opening!":     "hip:* & opening:*",
		"a'b & | ! :* (x)": "a:* & b:* & x:*",
		"раскрытие   ТБС":  "раскрытие:* & тбс:*",
	}
	for in, want := range cases {
		if got := BuildTSQuery(in); got != want {
			t.Errorf("BuildTSQuery(%q) = %q, want %q", in, got, want)
		}
	}
}
