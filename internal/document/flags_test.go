package document

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// TestComposeGotenbergFlags keeps the gotenberg command of both compose
// files equal to GotenbergFlags, which the integration suite runs (Q-101,
// Q-103): a flag added in one place only would ship untested.
func TestComposeGotenbergFlags(t *testing.T) {
	command := regexp.MustCompile(`(?s)\n  gotenberg:\n.*?\n    command: \[(.*?)\]`)
	arg := regexp.MustCompile(`"([^"]*)"`)
	for _, file := range []string{"../../deploy/sidecars.compose.yml", "../../docker-compose.dev.yml", "../../deploy/test/compose.yml"} {
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		m := command.FindSubmatch(b)
		if m == nil {
			t.Errorf("%s: no gotenberg command", file)
			continue
		}
		var got []string
		for _, a := range arg.FindAllSubmatch(m[1], -1) {
			got = append(got, string(a[1]))
		}
		if !slices.Equal(got, GotenbergFlags) {
			t.Errorf("%s: gotenberg command\n  %s\nwant document.GotenbergFlags\n  %s", file,
				strings.Join(got, " "), strings.Join(GotenbergFlags, " "))
		}
	}
}
