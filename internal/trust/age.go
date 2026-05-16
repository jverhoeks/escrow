package trust

import (
	"context"
	"fmt"
	"time"
)

type AgeSignal struct {
	minDays int
	now     func() time.Time
}

func NewAgeSignal(minDays int, nowFn func() time.Time) *AgeSignal {
	if nowFn == nil {
		nowFn = time.Now
	}
	return &AgeSignal{minDays: minDays, now: nowFn}
}

func (s *AgeSignal) Name() string { return "age" }

func (s *AgeSignal) Check(_ context.Context, pkg Package) (SignalReport, error) {
	ageDays := int(s.now().Sub(pkg.PublishedAt).Hours() / 24)
	if ageDays < s.minDays {
		return SignalReport{
			Signal: s.Name(),
			Result: SignalFail,
			Reason: fmt.Sprintf("published %d day(s) ago (minimum: %d)", ageDays, s.minDays),
		}, nil
	}
	return SignalReport{
		Signal: s.Name(),
		Result: SignalPass,
		Reason: fmt.Sprintf("%d days old", ageDays),
	}, nil
}
