package codebox

import (
	"fmt"
	"regexp"
	"strconv"
)

// mtuShape accepts a canonical 4-character decimal in 1200-1499 or exactly
// 1500, matching the bootstrap's glob `1[234][0-9][0-9] | 1500`. Leading
// zeros, negatives, and trailing garbage are rejected by construction.
var mtuShape = regexp.MustCompile(`^(1[2-4][0-9][0-9]|1500)$`)

// MTUAuto leaves the guest network MTU unchanged.
const MTUAuto = "auto"

// ValidateMTU checks TART_MTU on the host so a bad value fails fast, before a
// VM is booted. It accepts "auto" or an integer from 1280 to 1500 inclusive.
func ValidateMTU(s string) error {
	if s == MTUAuto {
		return nil
	}
	if !mtuShape.MatchString(s) {
		return fmt.Errorf("TART_MTU must be auto or an integer from 1280 to 1500")
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1280 {
		return fmt.Errorf("TART_MTU must be auto or an integer from 1280 to 1500")
	}
	return nil
}
