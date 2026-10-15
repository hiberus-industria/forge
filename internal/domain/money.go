package domain

import (
	"fmt"
	"math"
	"time"
)

// Micros is an amount of US dollars expressed in millionths (1 USD = 1_000_000).
// Integer arithmetic keeps aggregates exact; conversion to float only happens
// at presentation time.
type Micros int64

// USD returns the amount as a float number of dollars.
func (m Micros) USD() float64 { return float64(m) / 1e6 }

// MicrosFromUSD converts a dollar amount into Micros, rounding half away from zero.
func MicrosFromUSD(usd float64) Micros { return Micros(math.Round(usd * 1e6)) }

// String formats the amount as dollars with adaptive precision so that tiny
// per-request costs remain readable ($0.0042) while totals stay compact ($12.40).
func (m Micros) String() string { return FormatAmount(m.USD(), CurrencyUSD) }

// Currency is an ISO 4217 code supported for presentation.
type Currency string

const (
	CurrencyUSD Currency = "USD"
	CurrencyEUR Currency = "EUR"
)

// Symbol returns the display symbol of the currency.
func (c Currency) Symbol() string {
	if c == CurrencyEUR {
		return "€"
	}
	return "$"
}

// FormatAmount renders an amount with adaptive precision.
func FormatAmount(v float64, c Currency) string {
	abs := math.Abs(v)
	var s string
	switch {
	case abs == 0:
		s = "0.00"
	case abs < 0.01:
		s = fmt.Sprintf("%.4f", v)
	case abs < 100:
		s = fmt.Sprintf("%.2f", v)
	default:
		s = fmt.Sprintf("%.0f", v)
	}
	if c == CurrencyEUR {
		return s + " €"
	}
	return "$" + s
}

// FXRate is the ECB reference rate for a day: 1 EUR = USDPerEUR USD.
type FXRate struct {
	Date      time.Time
	USDPerEUR float64
}

// ToEUR converts a USD amount using the rate.
func (r FXRate) ToEUR(m Micros) float64 {
	if r.USDPerEUR <= 0 {
		return 0
	}
	return m.USD() / r.USDPerEUR
}
