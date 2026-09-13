package activities

import (
	"testing"
	"time"
)

func TestNormalizeDefaultsToPlanned(t *testing.T) {
	in := Input{Name: "  יריד  "}
	in.Normalize()

	if in.Name != "יריד" {
		t.Errorf("name = %q, want it trimmed", in.Name)
	}
	if in.Status != StatusPlanned {
		t.Errorf("status = %q, want the PLANNED default", in.Status)
	}
}

func TestValidateRejectsAnEndBeforeItsStart(t *testing.T) {
	start := time.Date(2026, 12, 2, 9, 0, 0, 0, time.UTC)
	end := start.Add(-time.Hour)

	in := Input{Name: "הפוך", StartAt: &start, EndAt: &end}
	in.Normalize()

	if _, ok := in.Validate()["end_at"]; !ok {
		t.Fatal("an end before its start should be reported on end_at")
	}
}

func TestValidateAllowsMissingOrPartialDates(t *testing.T) {
	start := time.Date(2026, 12, 1, 9, 0, 0, 0, time.UTC)

	cases := map[string]Input{
		"no dates":   {Name: "פרויקט"},
		"start only": {Name: "פרויקט", StartAt: &start},
		"end only":   {Name: "פרויקט", EndAt: &start},
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			in.Normalize()
			if problems := in.Validate(); len(problems) != 0 {
				t.Fatalf("dates are optional, got %v", problems)
			}
		})
	}
}

func TestValidateRejectsUnknownStatus(t *testing.T) {
	in := Input{Name: "פעילות", Status: "MAYBE"}
	in.Normalize()

	if _, ok := in.Validate()["status"]; !ok {
		t.Fatal("an unknown status should be reported on status")
	}
}

func TestEveryStatusHasAHebrewName(t *testing.T) {
	for _, status := range AllStatuses {
		if !status.Valid() {
			t.Errorf("%q is in AllStatuses but reports itself invalid", status)
		}
		name := status.HebrewName()
		if name == string(status) {
			t.Errorf("status %q has no Hebrew label", status)
		}
	}
}
