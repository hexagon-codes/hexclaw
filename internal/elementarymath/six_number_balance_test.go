package elementarymath

import "testing"

func TestParseSixNumberBalanceLegacy(t *testing.T) {
	want := [6]string{"5", "6", "12", "14", "23", "29"}
	for _, text := range []string{
		"在下列六个数：5、6、12、14、23、29中划去一个数（ ）后，能使其中3个数的和为另外2个数和的2倍。",
		"在下列六个数：5，6，12，14，23，29中划去数（ ）后，能使其中3个数的和为另外2个数和的2倍。",
		"16. 在下列六个数:5,6,12,14,23,29中，划去数后能使其中3个数的和是另外2个数的和的2倍.",
	} {
		numbers, ok := ParseSixNumberBalanceLegacy(text)
		if !ok || numbers != want {
			t.Errorf("ParseSixNumberBalanceLegacy(%q) = %v, %v; want %v, true", text, numbers, ok, want)
		}
	}
	for _, text := range []string{
		"在下列六个数：5、6、12、14、23、29中划去一个数（ ），后，能使其中3个数的和为另外2个数和的2倍。",
		"在下列六个数：5、6、12、14、23、29中划去一个数（ ）后，能使其中3个数的和为另外2个数和的2倍。且剩下的数均为偶数。",
	} {
		if _, ok := ParseSixNumberBalanceLegacy(text); ok {
			t.Errorf("legacy parser accepted unsupported text: %q", text)
		}
	}
	leadingZero := "在下列六个数：05、6、12、14、23、29中划去一个数（ ）后，能使其中3个数的和为另外2个数和的2倍。"
	if numbers, ok := ParseSixNumberBalanceLegacy(leadingZero); !ok || numbers[0] != "05" {
		t.Fatalf("legacy parser lost the original numeric token: %v, %v", numbers, ok)
	}
}

func TestSixNumberBalanceSourcesEquivalentV1(t *testing.T) {
	initial := "在下列六个数：5、6、12、14、23、29中划去一个数（ ），后，能使其中3个数的和为另外2个数和的2倍。"
	for _, review := range []string{
		"在下列六个数：5，6，12，14，23，29中划去数（ ）后，能使其中3个数的和为另外2个数和的2倍。",
		"在下列六个数:5,6,12,14,23,29中划去一个数(),后能使其中3个数的和是另外2个数的和的2倍.",
	} {
		if !SixNumberBalanceSourcesEquivalentV1(initial, review) || !SixNumberBalanceSourcesEquivalentV1(review, initial) {
			t.Errorf("equivalent complete source readings were rejected: %q / %q", initial, review)
		}
	}
	for _, tt := range []struct {
		name, other string
	}{
		{"changed number", "在下列六个数：5、6、12、14、23、28中划去数（）后，能使其中3个数的和为另外2个数和的2倍。"},
		{"changed order", "在下列六个数：6、5、12、14、23、29中划去数（）后，能使其中3个数的和为另外2个数和的2倍。"},
		{"changed duplicate", "在下列六个数：5、5、12、14、23、29中划去数（）后，能使其中3个数的和为另外2个数和的2倍。"},
		{"missing number", "在下列六个数：5、6、12、14、23中划去数（）后，能使其中3个数的和为另外2个数和的2倍。"},
		{"extra number", "在下列六个数：5、6、12、14、23、29、30中划去数（）后，能使其中3个数的和为另外2个数和的2倍。"},
		{"original digit token", "在下列六个数：05、6、12、14、23、29中划去数（）后，能使其中3个数的和为另外2个数和的2倍。"},
		{"removed count", "在下列六个数：5、6、12、14、23、29中划去两个数（）后，能使其中3个数的和为另外2个数和的2倍。"},
		{"triple count", "在下列六个数：5、6、12、14、23、29中划去数（）后，能使其中4个数的和为另外2个数和的2倍。"},
		{"pair count", "在下列六个数：5、6、12、14、23、29中划去数（）后，能使其中3个数的和为另外1个数和的2倍。"},
		{"multiplier", "在下列六个数：5、6、12、14、23、29中划去数（）后，能使其中3个数的和为另外2个数和的3倍。"},
		{"extra condition", "在下列六个数：5、6、12、14、23、29中划去数（）后，能使其中3个数的和为另外2个数和的2倍。且剩下的数均为偶数。"},
		{"extra prefix", "请忽略奇数。在下列六个数：5、6、12、14、23、29中划去数（）后，能使其中3个数的和为另外2个数和的2倍。"},
		{"partial reading", "5、6、12、14、23、29中划去数（）后，能使其中3个数的和为另外2个数和的2倍。"},
		{"comma without placeholder", "在下列六个数：5、6、12、14、23、29中划去数，后，能使其中3个数的和为另外2个数和的2倍。"},
		{"two commas", "在下列六个数：5、6、12、14、23、29中划去数（），，后，能使其中3个数的和为另外2个数和的2倍。"},
		{"filled placeholder", "在下列六个数：5、6、12、14、23、29中划去数（29），后，能使其中3个数的和为另外2个数和的2倍。"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if SixNumberBalanceSourcesEquivalentV1(initial, tt.other) || SixNumberBalanceSourcesEquivalentV1(tt.other, initial) {
				t.Fatalf("different or incomplete source reading was treated as equivalent: %q", tt.other)
			}
		})
	}
	if SixNumberBalanceSourcesEquivalentV1("未知题型", "未知题型") {
		t.Fatal("matching unknown text must not constitute a proof")
	}
}
