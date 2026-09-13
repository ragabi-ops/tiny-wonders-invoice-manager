package customers

import "testing"

func TestDigitsOnly(t *testing.T) {
	cases := map[string]string{
		"050-123-4567":     "0501234567",
		"050 123 4567":     "0501234567",
		"(050) 123-4567":   "0501234567",
		"+972-50-123-4567": "972501234567",
		"":                 "",
		"אין ספרות":        "",
	}
	for in, want := range cases {
		if got := DigitsOnly(in); got != want {
			t.Errorf("DigitsOnly(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeTrimsAndDefaults(t *testing.T) {
	in := Input{DisplayName: "  נועה כהן  ", Email: "  Noa@Example.COM "}
	in.Normalize()

	if in.DisplayName != "נועה כהן" {
		t.Errorf("display name = %q, want it trimmed", in.DisplayName)
	}
	// Lower-casing matters: the duplicate check compares emails directly.
	if in.Email != "noa@example.com" {
		t.Errorf("email = %q, want it lower-cased and trimmed", in.Email)
	}
	if in.CustomerType != TypePerson {
		t.Errorf("customer type = %q, want the PERSON default", in.CustomerType)
	}
	if in.PreferredDelivery != DeliveryNone {
		t.Errorf("delivery = %q, want the NONE default", in.PreferredDelivery)
	}
}

func TestValidateRequiresOnlyAName(t *testing.T) {
	// Demanding more would slow down adding a customer mid-conversation, which
	// is the common case (plan.md 8).
	in := Input{DisplayName: "לקוח"}
	in.Normalize()

	if problems := in.Validate(); len(problems) != 0 {
		t.Fatalf("a name alone should be enough, got %v", problems)
	}
}

func TestValidateReportsProblemsPerField(t *testing.T) {
	cases := []struct {
		name  string
		in    Input
		field string
	}{
		{"blank name", Input{DisplayName: "   "}, "display_name"},
		{"malformed email", Input{DisplayName: "לקוח", Email: "no-at-sign"}, "email"},
		{"unknown type", Input{DisplayName: "לקוח", CustomerType: "ROBOT"}, "customer_type"},
		{"unknown delivery", Input{DisplayName: "לקוח", PreferredDelivery: "PIGEON"}, "preferred_delivery"},
		{
			"email delivery without an address",
			Input{DisplayName: "לקוח", PreferredDelivery: DeliveryEmail},
			"email",
		},
		{
			"whatsapp delivery without a phone",
			Input{DisplayName: "לקוח", PreferredDelivery: DeliveryWhatsApp},
			"phone",
		},
		{
			"whatsapp delivery with a phone holding no digits",
			Input{DisplayName: "לקוח", PreferredDelivery: DeliveryWhatsApp, Phone: "---"},
			"phone",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			in := testCase.in
			in.Normalize()

			problems := in.Validate()
			message, ok := problems[testCase.field]
			if !ok {
				t.Fatalf("problems = %v, want one for %q", problems, testCase.field)
			}
			if !isHebrew(message) {
				t.Errorf("message %q is not Hebrew; it is shown to the user", message)
			}
		})
	}
}

func TestDuplicateReasonsHaveHebrewText(t *testing.T) {
	for _, reason := range []DuplicateReason{ReasonPhone, ReasonEmail, ReasonBusinessNumber, ReasonName} {
		if !isHebrew(reason.HebrewReason()) {
			t.Errorf("reason %q has no Hebrew explanation", reason)
		}
	}
}

func isHebrew(s string) bool {
	for _, r := range s {
		if r >= 0x0590 && r <= 0x05FF {
			return true
		}
	}
	return false
}
