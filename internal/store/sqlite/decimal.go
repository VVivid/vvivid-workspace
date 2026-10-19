package sqlite

import (
	"fmt"
	"strconv"
	"strings"
)

// Quantities are exact six-decimal fixed point, never floating point.
func decimalMicros(value string) (int64, error) {
	if !positiveDecimal.MatchString(value) {
		return 0, fmt.Errorf("quantity must have at most 9 integer and 6 fractional digits")
	}
	whole, fraction, _ := strings.Cut(value, ".")
	w, _ := strconv.ParseInt(whole, 10, 64)
	f, _ := strconv.ParseInt(fraction+strings.Repeat("0", 6-len(fraction)), 10, 64)
	return w*1_000_000 + f, nil
}

func PositiveDecimal(value string) bool { n, err := decimalMicros(value); return err == nil && n > 0 }

func NonnegativeDecimal(value string) bool { _, err := decimalMicros(value); return err == nil }

func AddDecimal(left, right string) (string, error) {
	l, err := decimalMicros(left)
	if err != nil {
		return "", err
	}
	r, err := decimalMicros(right)
	if err != nil {
		return "", err
	}
	sum := l + r
	if sum > 999_999_999_999_999 {
		return "", fmt.Errorf("%w: quantity exceeds supported range", ErrLimit)
	}
	return formatMicros(sum), nil
}

func SubtractDecimal(left, right string) (string, error) {
	l, err := decimalMicros(left)
	if err != nil {
		return "", err
	}
	r, err := decimalMicros(right)
	if err != nil || r > l {
		return "", fmt.Errorf("purchase quantity exceeds requested quantity")
	}
	return formatMicros(l - r), nil
}

func formatMicros(n int64) string {
	result := strconv.FormatInt(n/1_000_000, 10)
	if n%1_000_000 != 0 {
		result += "." + strings.TrimRight(fmt.Sprintf("%06d", n%1_000_000), "0")
	}
	return result
}
