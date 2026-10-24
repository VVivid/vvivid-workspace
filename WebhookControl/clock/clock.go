// Package clock supplies the wall clock for production port wiring.
package clock

import "time"

// Real implements ports.Clock. Tests can supply their own controllable clock.
type Real struct{}

func (Real) Now() time.Time                         { return time.Now() }
func (Real) After(d time.Duration) <-chan time.Time { return time.After(d) }
