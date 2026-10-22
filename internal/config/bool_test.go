package config

import "testing"

// A typo in .env must never arm a mode the user did not mean: only the
// explicit spellings count, everything else keeps the default.
func TestBoolFrom(t *testing.T) {
	env := map[string]string{
		"empty": "", "one": "1", "true": "TRUE", "yes": "yes", "on": "on",
		"zero": "0", "false": "false", "no": "no", "off": " Off ",
		"junk": "maybe", "number": "2",
	}
	get := func(k string) string { return env[k] }

	cases := []struct {
		key  string
		def  bool
		want bool
	}{
		{"empty", false, false},
		{"empty", true, true},
		{"one", false, true},
		{"true", false, true},
		{"yes", false, true},
		{"on", false, true},
		{"zero", true, false},
		{"false", true, false},
		{"no", true, false},
		{"off", true, false},
		{"junk", false, false},
		{"junk", true, true},
		{"number", false, false},
	}
	for _, c := range cases {
		if got := boolFrom(get, c.key, c.def); got != c.want {
			t.Fatalf("boolFrom(%q, def=%v) = %v, want %v", c.key, c.def, got, c.want)
		}
	}
}
