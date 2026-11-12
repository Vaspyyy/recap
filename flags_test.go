package main

import "testing"

// The tools agree on -V for the version and on how an unknown flag is told;
// -v is not recap's, so it is unknown rather than a guess.
func TestVersionAndUnknownFlag(t *testing.T) {
	for _, flag := range []string{"-V", "--version"} {
		if o, code, msg := parse([]string{flag}); o != nil || code != 0 || msg != "recap "+version+"\n" {
			t.Errorf("recap %s: code %d, msg %q", flag, code, msg)
		}
	}
	for _, flag := range []string{"-v", "--bogus"} {
		if _, code, msg := parse([]string{flag}); code != 2 || msg != "unknown flag "+flag+" (see recap --help)" {
			t.Errorf("recap %s: code %d, msg %q", flag, code, msg)
		}
	}
}
