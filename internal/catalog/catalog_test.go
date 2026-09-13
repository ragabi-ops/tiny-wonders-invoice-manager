package catalog

import (
	"testing"

	"github.com/ragabix/tiny-wonders-invoice-manager/internal/money"
)

func TestValidateRequiresANameAndANonNegativePrice(t *testing.T) {
	blank := Input{Name: "   "}
	blank.Normalize()
	if _, ok := blank.Validate()["name"]; !ok {
		t.Error("a blank name should be reported")
	}

	negative := Input{Name: "שירות", DefaultPrice: money.FromAgorot(-1)}
	negative.Normalize()
	if _, ok := negative.Validate()["default_price_agorot"]; !ok {
		t.Error("a negative price should be reported: a discount belongs on a document line")
	}

	// Zero is legitimate — a free item still belongs in the catalogue.
	free := Input{Name: "שירות", DefaultPrice: money.Zero}
	free.Normalize()
	if problems := free.Validate(); len(problems) != 0 {
		t.Errorf("a zero price should be accepted, got %v", problems)
	}
}
