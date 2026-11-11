package codebox

import "testing"

func TestValidateMTUAccepts(t *testing.T) {
	for _, v := range []string{"auto", "1280", "1400", "1499", "1500"} {
		if err := ValidateMTU(v); err != nil {
			t.Errorf("ValidateMTU(%q) = %v, want nil", v, err)
		}
	}
}

func TestValidateMTURejects(t *testing.T) {
	for _, v := range []string{"", "1279", "1501", "9000", "01280", "abc", "1280;id", "12 80", "-1280", "1.5"} {
		if err := ValidateMTU(v); err == nil {
			t.Errorf("ValidateMTU(%q) = nil, want error", v)
		}
	}
}
