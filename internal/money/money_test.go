package money

import (
	"encoding/json"
	"math"
	"testing"
)

func TestParse(t *testing.T) {
	cases := []struct {
		in   string
		want Amount
	}{
		{"0", 0},
		{"1", 100},
		{"1.00", 100},
		{"1.5", 150},
		{"1.05", 105},
		{"1234.56", 123456},
		{"1,234.56", 123456},
		{"₪1,234.56", 123456},
		{" 12.  ", 1200},
		{"-0.01", -1},
		{"-1,000", -100000},
		{"+7.25", 725},
		{".99", 99},
	}
	for _, c := range cases {
		got, err := Parse(c.in)
		if err != nil {
			t.Errorf("Parse(%q) returned error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("Parse(%q) = %d agorot, want %d", c.in, got, c.want)
		}
	}
}

func TestParseRejectsSubAgoraPrecision(t *testing.T) {
	// Rounding here would silently invent money; refusing is the correct answer.
	if _, err := Parse("1.234"); err == nil {
		t.Fatal("Parse(\"1.234\") should refuse more precision than one agora")
	}
}

func TestParseRejectsGarbage(t *testing.T) {
	for _, in := range []string{"", "abc", "1.2.3", "--1", "1e5"} {
		if _, err := Parse(in); err == nil {
			t.Errorf("Parse(%q) should have failed", in)
		}
	}
}

func TestString(t *testing.T) {
	cases := []struct {
		in   Amount
		want string
	}{
		{0, "0.00"},
		{5, "0.05"},
		{100, "1.00"},
		{123456, "1234.56"},
		{-1, "-0.01"},
		{-123456, "-1234.56"},
	}
	for _, c := range cases {
		if got := c.in.String(); got != c.want {
			t.Errorf("Amount(%d).String() = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestRoundTripThroughStringIsExact(t *testing.T) {
	// The classic float failure: 0.1 + 0.2 != 0.3. Integer agorot cannot fail it.
	tenth, _ := Parse("0.10")
	fifth, _ := Parse("0.20")
	sum, err := tenth.Add(fifth)
	if err != nil {
		t.Fatalf("Add returned error: %v", err)
	}
	if sum.String() != "0.30" {
		t.Fatalf("0.10 + 0.20 = %s, want 0.30", sum)
	}
}

func TestSumAndMul(t *testing.T) {
	price, _ := Parse("19.99")

	line, err := price.MulQuantity(3)
	if err != nil {
		t.Fatalf("MulQuantity returned error: %v", err)
	}
	if line.String() != "59.97" {
		t.Fatalf("19.99 x 3 = %s, want 59.97", line)
	}

	total, err := Sum(line, line, Zero)
	if err != nil {
		t.Fatalf("Sum returned error: %v", err)
	}
	if total.String() != "119.94" {
		t.Fatalf("total = %s, want 119.94", total)
	}
}

func TestMulQuantityByZero(t *testing.T) {
	price, _ := Parse("19.99")
	got, err := price.MulQuantity(0)
	if err != nil || got != Zero {
		t.Fatalf("MulQuantity(0) = %v, %v; want 0, nil", got, err)
	}
}

func TestOverflowIsReportedNotWrapped(t *testing.T) {
	max := Amount(math.MaxInt64)

	if _, err := max.Add(1); err == nil {
		t.Error("Add past MaxInt64 should report overflow")
	}
	if _, err := Amount(math.MinInt64).Sub(1); err == nil {
		t.Error("Sub past MinInt64 should report overflow")
	}
	if _, err := max.MulQuantity(2); err == nil {
		t.Error("MulQuantity past MaxInt64 should report overflow")
	}
}

func TestSubtraction(t *testing.T) {
	invoiced, _ := Parse("500.00")
	paid, _ := Parse("120.50")

	outstanding, err := invoiced.Sub(paid)
	if err != nil {
		t.Fatalf("Sub returned error: %v", err)
	}
	if outstanding.String() != "379.50" {
		t.Fatalf("outstanding = %s, want 379.50", outstanding)
	}

	overpaid, err := paid.Sub(invoiced)
	if err != nil {
		t.Fatalf("Sub returned error: %v", err)
	}
	if !overpaid.IsNegative() || overpaid.String() != "-379.50" {
		t.Fatalf("overpayment = %s, want -379.50", overpaid)
	}
}

func TestJSONIsAnIntegerOfAgorot(t *testing.T) {
	amount, _ := Parse("1234.56")

	encoded, err := json.Marshal(amount)
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}
	if string(encoded) != "123456" {
		t.Fatalf("encoded = %s, want 123456", encoded)
	}

	var decoded Amount
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("Unmarshal returned error: %v", err)
	}
	if decoded != amount {
		t.Fatalf("decoded = %d, want %d", decoded, amount)
	}

	// A decimal string must be refused: accepting it would invite a float.
	if err := json.Unmarshal([]byte(`"1234.56"`), &decoded); err == nil {
		t.Error("Unmarshal of a decimal string should fail")
	}
}

func TestMulQuantityMilli(t *testing.T) {
	price, _ := Parse("100.00")

	cases := []struct {
		quantityMilli int64
		want          string
	}{
		{1000, "100.00"}, // 1
		{1500, "150.00"}, // 1.5
		{500, "50.00"},   // 0.5
		{2250, "225.00"}, // 2.25
		{333, "33.30"},   // 0.333
		{1, "0.10"},      // 0.001
		{0, "0.00"},
	}
	for _, c := range cases {
		got, err := price.MulQuantityMilli(c.quantityMilli)
		if err != nil {
			t.Errorf("MulQuantityMilli(%d) returned error: %v", c.quantityMilli, err)
			continue
		}
		if got.String() != c.want {
			t.Errorf("100.00 x %d/1000 = %s, want %s", c.quantityMilli, got, c.want)
		}
	}
}

func TestMulQuantityMilliRoundsHalfAwayFromZero(t *testing.T) {
	// 33.33 x 1.5 = 49.995, which must become 50.00 rather than 49.99.
	price, _ := Parse("33.33")
	got, err := price.MulQuantityMilli(1500)
	if err != nil {
		t.Fatalf("MulQuantityMilli returned error: %v", err)
	}
	if got.String() != "50.00" {
		t.Fatalf("33.33 x 1.5 = %s, want 50.00", got)
	}

	// 0.01 x 0.5 = 0.005, exactly half an agora, which rounds up.
	agora, _ := Parse("0.01")
	got, err = agora.MulQuantityMilli(500)
	if err != nil {
		t.Fatalf("MulQuantityMilli returned error: %v", err)
	}
	if got.String() != "0.01" {
		t.Fatalf("0.01 x 0.5 = %s, want 0.01", got)
	}
}

func TestMulQuantityMilliRejectsNegativeQuantity(t *testing.T) {
	price, _ := Parse("10.00")
	if _, err := price.MulQuantityMilli(-1000); err == nil {
		t.Fatal("a negative quantity should be refused")
	}
}

func TestDocumentTotalEqualsSumOfRoundedLines(t *testing.T) {
	// The property that matters on a printed document: the total must equal the
	// sum of the line amounts a reader can see, not a re-rounded raw product.
	price, _ := Parse("33.33")

	var lines []Amount
	for range 3 {
		line, err := price.MulQuantityMilli(1500)
		if err != nil {
			t.Fatalf("MulQuantityMilli returned error: %v", err)
		}
		lines = append(lines, line)
	}

	total, err := Sum(lines...)
	if err != nil {
		t.Fatalf("Sum returned error: %v", err)
	}
	if total.String() != "150.00" {
		t.Fatalf("total = %s, want 150.00 (three lines of 50.00)", total)
	}
}

func TestFormatQuantityMilli(t *testing.T) {
	cases := map[int64]string{
		1000:  "1",
		1500:  "1.5",
		2250:  "2.25",
		333:   "0.333",
		1:     "0.001",
		0:     "0",
		10500: "10.5",
	}
	for in, want := range cases {
		if got := FormatQuantityMilli(in); got != want {
			t.Errorf("FormatQuantityMilli(%d) = %q, want %q", in, got, want)
		}
	}
}
