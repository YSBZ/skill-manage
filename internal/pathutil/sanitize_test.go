package pathutil

import "testing"

func TestSanitizePathName(t *testing.T) {
	cases := map[string]string{
		"ce-plan":      "ce-plan",      // already safe — unchanged
		"ce:plan":      "ce-plan",      // colon → dash
		"a/b":          "a-b",          // slash → dash
		`weird*name?`:  "weird-name-",  // glob chars → dash
		"trailing.":    "trailing",     // trailing dot stripped
		"trailing ":    "trailing",     // trailing space stripped
		"CON":          "_CON",         // reserved device name guarded
		"com1":         "_com1",        // reserved (case-insensitive)
		"nul.txt":      "_nul.txt",     // reserved base with extension
		"lpt9":         "_lpt9",        // reserved
		"comx":         "comx",         // not reserved (x is not 1-9)
		"normal.skill": "normal.skill", // interior dot fine
	}
	for in, want := range cases {
		if got := SanitizePathName(in); got != want {
			t.Errorf("SanitizePathName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSanitizePathNameEmpty(t *testing.T) {
	if got := SanitizePathName("..."); got != "_" {
		t.Errorf("all-dots name should sanitize to %q, got %q", "_", got)
	}
}
