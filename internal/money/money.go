// Package money represents ILS amounts as integer agorot. Floating point never
// touches a monetary value anywhere in this system (plan.md 3.6).
package money

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Amount is a signed number of agorot. 1 ILS = 100 agorot.
type Amount int64

// Currency is fixed for v1. Multi-currency is not a goal (plan.md 21).
const Currency = "ILS"

// Zero is the additive identity.
const Zero Amount = 0

// ErrOverflow means an operation exceeded the int64 range. At realistic
// business amounts this cannot happen, but a silent wraparound in a financial
// total is unacceptable, so it is checked rather than assumed.
var ErrOverflow = errors.New("money: amount overflow")

// FromAgorot builds an Amount from agorot.
func FromAgorot(agorot int64) Amount { return Amount(agorot) }

// FromShekels builds an Amount from whole shekels.
func FromShekels(shekels int64) (Amount, error) {
	if shekels > (1<<63-1)/100 || shekels < -(1<<63)/100 {
		return 0, ErrOverflow
	}
	return Amount(shekels * 100), nil
}

// Agorot returns the raw agorot, which is what the database stores.
func (a Amount) Agorot() int64 { return int64(a) }

// IsZero reports whether the amount is exactly zero.
func (a Amount) IsZero() bool { return a == 0 }

// IsNegative reports whether the amount is below zero.
func (a Amount) IsNegative() bool { return a < 0 }

// Add returns a+b, refusing to wrap around.
func (a Amount) Add(b Amount) (Amount, error) {
	sum := a + b
	// Overflow happened if the operands share a sign that the result does not.
	if (a > 0 && b > 0 && sum < 0) || (a < 0 && b < 0 && sum > 0) {
		return 0, ErrOverflow
	}
	return sum, nil
}

// Sub returns a-b, refusing to wrap around.
func (a Amount) Sub(b Amount) (Amount, error) {
	if b == -(1 << 63) {
		return 0, ErrOverflow
	}
	return a.Add(-b)
}

// MulQuantity multiplies a unit price by an integer quantity. Quantities are
// integers in v1; a fractional-quantity line would need an explicit rounding
// rule, which is a compliance question, not an implementation detail.
func (a Amount) MulQuantity(quantity int64) (Amount, error) {
	if quantity == 0 || a == 0 {
		return 0, nil
	}
	product := Amount(int64(a) * quantity)
	// Verify by division: exact iff no overflow occurred.
	if int64(product)/quantity != int64(a) {
		return 0, ErrOverflow
	}
	return product, nil
}

// QuantityScale is the fixed-point scale for quantities: they are integer
// thousandths, so 1.5 hours is 1500. Exact, like money.
const QuantityScale int64 = 1000

// MulQuantityMilli multiplies a unit price by a quantity expressed in
// thousandths, rounding the result to whole agorot.
//
// Rounding is half away from zero — 0.5 agora rounds up, -0.5 down — which is
// the arithmetic convention a person doing this by hand would use. It is
// applied once, per line; totals are then exact sums of already-rounded lines,
// so a document's total always equals the sum of the lines printed on it.
func (a Amount) MulQuantityMilli(quantityMilli int64) (Amount, error) {
	if quantityMilli == 0 || a == 0 {
		return 0, nil
	}
	if quantityMilli < 0 {
		return 0, fmt.Errorf("money: quantity must not be negative")
	}

	product := int64(a) * quantityMilli
	if product/quantityMilli != int64(a) {
		return 0, ErrOverflow
	}

	// Half away from zero, using integer arithmetic only.
	half := QuantityScale / 2
	if product >= 0 {
		return Amount((product + half) / QuantityScale), nil
	}
	return Amount((product - half) / QuantityScale), nil
}

// FormatQuantityMilli renders a quantity in thousandths as a decimal string
// with no trailing zeros: 1500 becomes "1.5", 2000 becomes "2".
func FormatQuantityMilli(quantityMilli int64) string {
	whole := quantityMilli / QuantityScale
	frac := quantityMilli % QuantityScale

	sign := ""
	if quantityMilli < 0 {
		sign = "-"
		whole, frac = -whole, -frac
	}
	if frac == 0 {
		return fmt.Sprintf("%s%d", sign, whole)
	}

	fraction := strings.TrimRight(fmt.Sprintf("%03d", frac), "0")
	return fmt.Sprintf("%s%d.%s", sign, whole, fraction)
}

// Sum adds amounts left to right, failing on overflow.
func Sum(amounts ...Amount) (Amount, error) {
	total := Zero
	for _, amount := range amounts {
		var err error
		if total, err = total.Add(amount); err != nil {
			return 0, err
		}
	}
	return total, nil
}

// String renders the amount as a plain decimal with two places, for logs and
// CSV exports. User-facing formatting with ₪ and Hebrew separators happens in
// the browser via Intl.NumberFormat.
func (a Amount) String() string {
	sign := ""
	value := int64(a)
	if value < 0 {
		sign = "-"
		value = -value
	}
	return fmt.Sprintf("%s%d.%02d", sign, value/100, value%100)
}

// Parse reads "1234.56", "1234", "1,234.56" or "₪1,234.56" into agorot.
// It is exact: the fractional part is parsed as digits, never as a float.
func Parse(s string) (Amount, error) {
	s = strings.TrimSpace(s)
	s = strings.NewReplacer("₪", "", ",", "", " ", "", " ", "").Replace(s)
	if s == "" {
		return 0, fmt.Errorf("money: empty amount")
	}

	negative := false
	switch s[0] {
	case '-':
		negative, s = true, s[1:]
	case '+':
		s = s[1:]
	}

	whole, frac, hasFrac := strings.Cut(s, ".")
	if whole == "" {
		whole = "0"
	}

	// ParseInt would accept a second sign ("--1") and a further dot would be
	// silently ignored, so both are rejected explicitly.
	if !isDigits(whole) || (hasFrac && !isDigits(frac) && frac != "") {
		return 0, fmt.Errorf("money: invalid amount %q", s)
	}

	wholeValue, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("money: invalid amount %q", s)
	}

	var fracValue int64
	if hasFrac {
		switch len(frac) {
		case 0:
			// "12." is accepted as 12.00.
		case 1:
			// "12.5" means 50 agorot, not 5.
			if fracValue, err = strconv.ParseInt(frac, 10, 64); err != nil {
				return 0, fmt.Errorf("money: invalid amount %q", s)
			}
			fracValue *= 10
		case 2:
			if fracValue, err = strconv.ParseInt(frac, 10, 64); err != nil {
				return 0, fmt.Errorf("money: invalid amount %q", s)
			}
		default:
			// Refuse rather than round: the caller decides how to handle
			// sub-agora precision, if it ever becomes legal to.
			return 0, fmt.Errorf("money: %q has more precision than one agora", s)
		}
	}

	total, err := FromShekels(wholeValue)
	if err != nil {
		return 0, err
	}
	if total, err = total.Add(Amount(fracValue)); err != nil {
		return 0, err
	}
	if negative {
		total = -total
	}
	return total, nil
}

// MarshalJSON emits agorot as a JSON integer, so no client can reintroduce
// floating-point error by round-tripping a decimal string.
func (a Amount) MarshalJSON() ([]byte, error) {
	return json.Marshal(int64(a))
}

// UnmarshalJSON accepts a JSON integer of agorot.
func (a *Amount) UnmarshalJSON(data []byte) error {
	var agorot int64
	if err := json.Unmarshal(data, &agorot); err != nil {
		return fmt.Errorf("money: expected an integer number of agorot: %w", err)
	}
	*a = Amount(agorot)
	return nil
}

// isDigits reports whether s consists only of ASCII digits.
func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
