//go:build integration

package itest

import (
	"sort"
	"strings"
	"testing"

	"github.com/ovander/tenure-api/internal/httpapi"
)

// TestZAuditedRoutes runs last (files and tests run in name order): the list of
// audited routes equals the router's mutating routes, and every one of them
// was exercised by a successful, audited call (RENT-BE-001 §8b).
func TestZAuditedRoutes(t *testing.T) {
	listed := auditedRoutes(t)
	var want, got []string
	for _, rt := range httpapi.Routes(httpapi.Deps{}) {
		if rt.Method != "GET" {
			want = append(want, rt.Method+" "+rt.Pattern)
		}
	}
	for r := range listed {
		got = append(got, r)
	}
	sort.Strings(want)
	sort.Strings(got)
	if strings.Join(want, "\n") != strings.Join(got, "\n") {
		t.Errorf("testdata/audited_routes.txt\n%s\ndiffers from the router's mutating routes\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	for r := range listed {
		if !seen[r] {
			t.Errorf("%s was never exercised with a successful, audited call", r)
		}
	}
}
