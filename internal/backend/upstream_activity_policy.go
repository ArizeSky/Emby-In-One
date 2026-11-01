package backend

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Upstream activity reminder rules: interval validation, UTC calendar
// arithmetic, manual/observed reference selection and risk state computation.
// Everything here is pure: no database, no clock, no configuration access.

const (
	UpstreamActivityUnitDays   = "days"
	UpstreamActivityUnitMonths = "months"

	UpstreamActivityMinIntervalDays   = 1
	UpstreamActivityMaxIntervalDays   = 3650
	UpstreamActivityMinIntervalMonths = 1
	UpstreamActivityMaxIntervalMonths = 120
	UpstreamActivityMinWatchSeconds   = 10
	UpstreamActivityMaxWatchSeconds   = 3600

	UpstreamActivityDefaultIntervalDays    = 30
	UpstreamActivityDefaultMinWatchSeconds = 60
)

// Risk states. unknown and unavailable are deliberately distinct from normal:
// missing evidence never renders green.
const (
	UpstreamActivityStateDisabled    = "disabled"
	UpstreamActivityStateUnknown     = "unknown"
	UpstreamActivityStateNormal      = "normal"
	UpstreamActivityStateWarning     = "warning"
	UpstreamActivityStateUrgent      = "urgent"
	UpstreamActivityStateOverdue     = "overdue"
	UpstreamActivityStateUnavailable = "unavailable"
)

// Reference sources for the reminder baseline.
const (
	UpstreamActivityReferenceObserved = "observed"
	UpstreamActivityReferenceManual   = "manual"
)

// Diagnostic reasons carried by a view. They are non-secret and may be shown to
// administrators, but never substitute for a state of their own.
const (
	UpstreamActivityReasonNoReference       = "no_reference"
	UpstreamActivityReasonReferenceInFuture = "reference_in_future"
	UpstreamActivityReasonInvalidInterval   = "invalid_interval"
	UpstreamActivityReasonStoreUnavailable  = "store_unavailable"
)

// UpstreamActivityMaxRevision bounds stored revisions to 2^53-1 so they stay
// exactly representable as JavaScript/JSON numbers (K3 may still serialize them
// as a decimal string). Revisions are opaque random values, never derived from
// the account scope.
const UpstreamActivityMaxRevision int64 = 1<<53 - 1

var (
	// ErrUpstreamActivityInvalid covers malformed policy input: unknown units,
	// out-of-range numbers, non-positive times and future manual references.
	ErrUpstreamActivityInvalid = errors.New("upstream activity: invalid policy")
	// ErrUpstreamActivityRevisionConflict is the optimistic-lock rejection of a
	// stale settings form.
	ErrUpstreamActivityRevisionConflict = errors.New("upstream activity: revision conflict")
	// ErrUpstreamActivityScopeMismatch rejects writes or reads issued for an
	// account range that no longer owns the server's summary. Latent events from
	// an old account must never be recorded against the current one.
	ErrUpstreamActivityScopeMismatch = errors.New("upstream activity: account scope mismatch")
	// ErrUpstreamActivityUnavailable means no shared SQLite store is wired (or the
	// store handle is closed). Callers must report data as unavailable instead of
	// treating it as disabled/healthy.
	ErrUpstreamActivityUnavailable = errors.New("upstream activity: store unavailable")
)

// UpstreamActivityPolicy is the per-server reminder configuration. The manual
// reference is stored as UTC Unix milliseconds; the account scope it was set
// under is kept internal and never serialized.
type UpstreamActivityPolicy struct {
	Enabled           bool   `json:"enabled"`
	IntervalUnit      string `json:"intervalUnit"`
	IntervalValue     int    `json:"intervalValue"`
	MinWatchSeconds   int    `json:"minWatchSeconds"`
	ManualReferenceAt *int64 `json:"manualReferenceAt"`
	ManualScope       string `json:"-"`
	Revision          int64  `json:"revision"`
}

// UpstreamActivityView is the server-computed status handed to the admin API.
// The account scope digest is intentionally absent: clients only see revision
// and timestamps.
type UpstreamActivityView struct {
	Available         bool     `json:"available"`
	State             string   `json:"state"`
	Reason            string   `json:"reason,omitempty"`
	Enabled           bool     `json:"enabled"`
	IntervalUnit      string   `json:"intervalUnit"`
	IntervalValue     int      `json:"intervalValue"`
	MinWatchSeconds   int      `json:"minWatchSeconds"`
	Revision          int64    `json:"revision"`
	LastActivityAt    *int64   `json:"lastActivityAt"`
	LastQualifiedAt   *int64   `json:"lastQualifiedAt"`
	ManualReferenceAt *int64   `json:"manualReferenceAt"`
	ReferenceAt       *int64   `json:"referenceAt"`
	ReferenceSource   string   `json:"referenceSource,omitempty"`
	DueAt             *int64   `json:"dueAt"`
	RemainingRatio    *float64 `json:"remainingRatio"`
}

// DefaultUpstreamActivityPolicy returns the documented default: reminders off,
// 30-day cycle, 60 seconds minimum watch time, no manual reference.
func DefaultUpstreamActivityPolicy() UpstreamActivityPolicy {
	return UpstreamActivityPolicy{
		Enabled:         false,
		IntervalUnit:    UpstreamActivityUnitDays,
		IntervalValue:   UpstreamActivityDefaultIntervalDays,
		MinWatchSeconds: UpstreamActivityDefaultMinWatchSeconds,
	}
}

// NormalizeUpstreamActivityIntervalUnit accepts only the documented units.
// Surrounding whitespace and case are forgiven; everything else is rejected.
func NormalizeUpstreamActivityIntervalUnit(unit string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(unit)) {
	case UpstreamActivityUnitDays:
		return UpstreamActivityUnitDays, nil
	case UpstreamActivityUnitMonths:
		return UpstreamActivityUnitMonths, nil
	default:
		return "", fmt.Errorf("%w: unknown interval unit %q", ErrUpstreamActivityInvalid, unit)
	}
}

// NormalizeUpstreamActivityPolicy canonicalizes the unit and validates the
// policy against the supplied server time. A manual reference in the future is
// rejected rather than treated as safe.
func NormalizeUpstreamActivityPolicy(p UpstreamActivityPolicy, nowMillis int64) (UpstreamActivityPolicy, error) {
	unit, err := NormalizeUpstreamActivityIntervalUnit(p.IntervalUnit)
	if err != nil {
		return p, err
	}
	p.IntervalUnit = unit
	if err := validateUpstreamActivityIntervalValue(unit, p.IntervalValue); err != nil {
		return p, err
	}
	if p.MinWatchSeconds < UpstreamActivityMinWatchSeconds || p.MinWatchSeconds > UpstreamActivityMaxWatchSeconds {
		return p, fmt.Errorf("%w: minWatchSeconds %d out of range %d..%d", ErrUpstreamActivityInvalid,
			p.MinWatchSeconds, UpstreamActivityMinWatchSeconds, UpstreamActivityMaxWatchSeconds)
	}
	if p.ManualReferenceAt != nil {
		if *p.ManualReferenceAt <= 0 {
			return p, fmt.Errorf("%w: manualReferenceAt must be a positive UTC time", ErrUpstreamActivityInvalid)
		}
		if *p.ManualReferenceAt > nowMillis {
			return p, fmt.Errorf("%w: manualReferenceAt is in the future", ErrUpstreamActivityInvalid)
		}
	}
	return p, nil
}

func validateUpstreamActivityIntervalValue(unit string, value int) error {
	switch unit {
	case UpstreamActivityUnitDays:
		if value < UpstreamActivityMinIntervalDays || value > UpstreamActivityMaxIntervalDays {
			return fmt.Errorf("%w: intervalValue %d out of range %d..%d days", ErrUpstreamActivityInvalid,
				value, UpstreamActivityMinIntervalDays, UpstreamActivityMaxIntervalDays)
		}
	case UpstreamActivityUnitMonths:
		if value < UpstreamActivityMinIntervalMonths || value > UpstreamActivityMaxIntervalMonths {
			return fmt.Errorf("%w: intervalValue %d out of range %d..%d months", ErrUpstreamActivityInvalid,
				value, UpstreamActivityMinIntervalMonths, UpstreamActivityMaxIntervalMonths)
		}
	default:
		return fmt.Errorf("%w: unknown interval unit %q", ErrUpstreamActivityInvalid, unit)
	}
	return nil
}

// AddUpstreamActivityInterval adds one reminder cycle to a UTC reference time.
// Days are fixed 24-hour periods. Months are rolling UTC calendar months with
// the day clamped to the target month's last day, so Jan 31 + 1 month is Feb 28
// (Feb 29 in a leap year) and never silently rolls into March.
func AddUpstreamActivityInterval(referenceMillis int64, unit string, value int) (int64, error) {
	if referenceMillis <= 0 {
		return 0, fmt.Errorf("%w: reference time must be positive", ErrUpstreamActivityInvalid)
	}
	normalized, err := NormalizeUpstreamActivityIntervalUnit(unit)
	if err != nil {
		return 0, err
	}
	if err := validateUpstreamActivityIntervalValue(normalized, value); err != nil {
		return 0, err
	}
	reference := time.UnixMilli(referenceMillis).UTC()
	if normalized == UpstreamActivityUnitDays {
		return reference.Add(time.Duration(value) * 24 * time.Hour).UnixMilli(), nil
	}
	return addUpstreamActivityMonthsUTC(reference, value).UnixMilli(), nil
}

func addUpstreamActivityMonthsUTC(reference time.Time, months int) time.Time {
	year, month, day := reference.Date()
	hour, minute, second := reference.Clock()
	nanosecond := reference.Nanosecond()

	totalMonths := year*12 + int(month) - 1 + months
	targetYear := floorDiv(totalMonths, 12)
	targetMonth := time.Month(totalMonths-targetYear*12) + 1
	if lastDay := upstreamActivityLastDayOfMonthUTC(targetYear, targetMonth); day > lastDay {
		day = lastDay
	}
	return time.Date(targetYear, targetMonth, day, hour, minute, second, nanosecond, time.UTC)
}

func upstreamActivityLastDayOfMonthUTC(year int, month time.Month) int {
	// Day 0 of the following month normalizes to the last day of month.
	return time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

func floorDiv(a, b int) int {
	quotient := a / b
	if a%b != 0 && (a < 0) != (b < 0) {
		quotient--
	}
	return quotient
}

// UpstreamActivityReferenceAt picks the reminder baseline: max(observed,
// manual). A manual reference only wins when it is strictly newer, matching the
// "manual" label rule; nil means there is no evidence yet.
func UpstreamActivityReferenceAt(lastQualifiedAt, manualReferenceAt *int64) (*int64, string) {
	var reference *int64
	source := ""
	if isPositiveMillis(lastQualifiedAt) {
		value := *lastQualifiedAt
		reference = &value
		source = UpstreamActivityReferenceObserved
	}
	if isPositiveMillis(manualReferenceAt) && (reference == nil || *manualReferenceAt > *reference) {
		value := *manualReferenceAt
		reference = &value
		source = UpstreamActivityReferenceManual
	}
	return reference, source
}

func isPositiveMillis(value *int64) bool {
	return value != nil && *value > 0
}

func cloneMillis(value *int64) *int64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

// ComputeUpstreamActivityView computes the authoritative reminder state from a
// policy, a summary snapshot, the server time and a store-availability flag.
//
// Precedence is unavailable > disabled > unknown > risk tier, so neither a
// database failure nor missing/future evidence can render green. A disabled
// policy still exposes the observed times plus, when derivable, the schedule.
func ComputeUpstreamActivityView(policy UpstreamActivityPolicy, summary UpstreamActivitySummary, nowMillis int64, available bool) UpstreamActivityView {
	view := UpstreamActivityView{
		Available:         available,
		Enabled:           policy.Enabled,
		IntervalUnit:      policy.IntervalUnit,
		IntervalValue:     policy.IntervalValue,
		MinWatchSeconds:   policy.MinWatchSeconds,
		Revision:          policy.Revision,
		LastActivityAt:    cloneMillis(summary.LastActivityAt),
		LastQualifiedAt:   cloneMillis(summary.LastQualifiedAt),
		ManualReferenceAt: cloneMillis(policy.ManualReferenceAt),
	}
	if !available {
		view.State = UpstreamActivityStateUnavailable
		view.Reason = UpstreamActivityReasonStoreUnavailable
		return view
	}

	reference, source := UpstreamActivityReferenceAt(summary.LastQualifiedAt, policy.ManualReferenceAt)
	view.ReferenceAt = reference
	view.ReferenceSource = source

	due, ratio, scheduleReason := computeUpstreamActivitySchedule(policy, reference, nowMillis)
	view.DueAt = due
	view.RemainingRatio = ratio

	if !policy.Enabled {
		view.State = UpstreamActivityStateDisabled
		view.Reason = scheduleReason
		return view
	}
	if reference == nil {
		view.State = UpstreamActivityStateUnknown
		view.Reason = UpstreamActivityReasonNoReference
		return view
	}
	// A reference ahead of the server clock means the clock moved backwards or
	// the observation is bogus. That is unknown, never safe.
	if *reference > nowMillis {
		view.State = UpstreamActivityStateUnknown
		view.Reason = UpstreamActivityReasonReferenceInFuture
		view.DueAt = nil
		view.RemainingRatio = nil
		return view
	}
	if scheduleReason != "" {
		view.State = UpstreamActivityStateUnknown
		view.Reason = scheduleReason
		return view
	}
	switch {
	case ratio != nil && *ratio > 0.5:
		view.State = UpstreamActivityStateNormal
	case ratio != nil && *ratio > 0.25:
		view.State = UpstreamActivityStateWarning
	case ratio != nil && *ratio > 0:
		view.State = UpstreamActivityStateUrgent
	default:
		view.State = UpstreamActivityStateOverdue
	}
	return view
}

// computeUpstreamActivitySchedule derives dueAt and remainingRatio. An invalid
// interval returns a diagnostic and no schedule instead of a bogus ratio.
func computeUpstreamActivitySchedule(policy UpstreamActivityPolicy, reference *int64, nowMillis int64) (*int64, *float64, string) {
	if reference == nil {
		return nil, nil, ""
	}
	due, err := AddUpstreamActivityInterval(*reference, policy.IntervalUnit, policy.IntervalValue)
	if err != nil {
		return nil, nil, UpstreamActivityReasonInvalidInterval
	}
	duration := due - *reference
	if duration <= 0 {
		return nil, nil, UpstreamActivityReasonInvalidInterval
	}
	ratio := float64(due-nowMillis) / float64(duration)
	return &due, &ratio, ""
}

// UpstreamActivityTimeRFC3339 renders a stored UTC millisecond timestamp for the
// API. A nil timestamp stays null: callers must never display 1970.
func UpstreamActivityTimeRFC3339(millis *int64) *string {
	if !isPositiveMillis(millis) {
		return nil
	}
	formatted := time.UnixMilli(*millis).UTC().Format(time.RFC3339)
	return &formatted
}

// ParseUpstreamActivityTimeRFC3339 parses an API timestamp into UTC millis.
// Fractional seconds are accepted; the result is normalized to UTC.
func ParseUpstreamActivityTimeRFC3339(value string) (*int64, error) {
	parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(value))
	if err != nil {
		return nil, fmt.Errorf("%w: manualReferenceAt: %v", ErrUpstreamActivityInvalid, err)
	}
	millis := parsed.UTC().UnixMilli()
	return &millis, nil
}
