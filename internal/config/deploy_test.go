package config

import (
	"bufio"
	"os"
	"regexp"
	"strings"
	"testing"
)

// The production template deploy/tenure.env.example, its placeholders filled
// with fake values, passes validation and names every setting tenure reads
// (except the two that only a dev or a test sets, the optional audience and
// the former names, which are refused).
func TestDeployTemplate(t *testing.T) {
	f, err := os.Open("../../deploy/tenure.env.example")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	m := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		k, v, ok := strings.Cut(l, "=")
		if !ok {
			t.Fatalf("not KEY=value: %q", l)
		}
		if k == "TENURE_SESSION_KEY" {
			v = strings.ReplaceAll(v, "<set-me>", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=") // 32 zero bytes
		}
		m[k] = strings.ReplaceAll(v, "<set-me>", "placeholder")
	}
	if _, err := Load(env(m), NeedAPI|NeedJobs|NeedMigrate|NeedDocuments); err != nil {
		t.Fatalf("the template does not validate: %v", err)
	}
	src, err := os.ReadFile("config.go")
	if err != nil {
		t.Fatal(err)
	}
	optional := map[string]bool{"TENURE_S3_INSECURE": true, "TENURE_IRL_SOURCE_URL": true, "SOCRATE_AUDIENCE": true,
		"TENURE_YOUSIGN_API_URL": true, "TENURE_YOUSIGN_API_KEY": true}
	for _, k := range regexp.MustCompile(`"((?:TENURE|SOCRATE)_[A-Z0-9_]+)"`).FindAllStringSubmatch(string(src), -1) {
		if _, old := RenamedSocrate[k[1]]; old {
			if _, set := m[k[1]]; set {
				t.Errorf("deploy/tenure.env.example uses the former name %s", k[1])
			}
			continue
		}
		if _, ok := m[k[1]]; !ok && !optional[k[1]] {
			t.Errorf("deploy/tenure.env.example lacks %s", k[1])
		}
	}
}
