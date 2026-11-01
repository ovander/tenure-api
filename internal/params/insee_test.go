package params

import (
	"strings"
	"testing"

	"github.com/ovander/tenure-api/internal/period"
)

const sample = `<?xml version="1.0" encoding="UTF-8"?>
<message:StructureSpecificData xmlns:message="http://www.sdmx.org/resources/sdmxml/schemas/v2_1/message">
<message:DataSet><Series IDBANK="001515333" FREQ="T">
<Obs TIME_PERIOD="2025-Q2" OBS_VALUE="146.68" OBS_STATUS="A"/>
<Obs TIME_PERIOD="2025-Q1" OBS_VALUE="145.47"/>
<Obs TIME_PERIOD="2024-Q4" OBS_VALUE="144.6"/>
</Series></message:DataSet></message:StructureSpecificData>`

func TestParseSDMX(t *testing.T) {
	got, err := ParseSDMX(strings.NewReader(sample))
	if err != nil {
		t.Fatal(err)
	}
	if got["2025-Q2"] != 14668 || got["2025-Q1"] != 14547 || got["2024-Q4"] != 14460 || len(got) != 3 {
		t.Errorf("%v", got)
	}
	for _, bad := range []string{`<a/>`, `<Obs TIME_PERIOD="2025-Q2" OBS_VALUE="1.234"/>`, `<Obs TIME_PERIOD="2025-Q2" OBS_VALUE="-1"/>`} {
		if _, err := ParseSDMX(strings.NewReader(bad)); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}

func TestEstimatedPublication(t *testing.T) {
	today := period.NewDate(2026, 11, 1)
	for q, want := range map[string]period.Date{"2025-Q2": period.NewDate(2025, 7, 15), "2025-Q4": period.NewDate(2026, 1, 15),
		"2026-Q3": period.NewDate(2026, 10, 15), "2026-Q4": today} {
		if got := EstimatedPublication(q, today); got != want {
			t.Errorf("%s: %s; want %s", q, got, want)
		}
	}
}
