package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRunRefusesWithoutConfig(t *testing.T) {
	none := func(string) string { return "" }
	var out bytes.Buffer
	cases := map[string]string{
		"serve":            "SOCRATE_ISSUER is required",
		"migrate up":       "TENURE_MIGRATE_DB_URL is required",
		"job audit.verify": "TENURE_JOBS_DB_URL is required",
		"":                 "usage",
		"dance":            "usage",
	}
	for args, want := range cases {
		err := run(context.Background(), strings.Fields(args), none, &out)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v; want %q", args, err, want)
		}
	}
	if err := run(context.Background(), []string{"version"}, none, &out); err != nil || out.Len() == 0 {
		t.Errorf("version: %v %q", err, out.String())
	}
}
