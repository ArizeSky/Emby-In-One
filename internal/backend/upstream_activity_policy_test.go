package backend

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

const upstreamActivityTestDayMillis int64 = 24 * 60 * 60 * 1000

func upstreamActivityTestMillis(t *testing.T, value string) int64 {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatalf("parse %q: %v", value, err)
	}
	return parsed.UTC().UnixMilli()
}

func upstreamActivityTestPolicy() UpstreamActivityPolicy {
	return UpstreamActivityPolicy{
		Enabled:         true,
		IntervalUnit:    UpstreamActivityUnitDays,
		IntervalValue:   30,
		MinWatchSeconds: UpstreamActivityDefaultMinWatchSeconds,
	}
}

func TestUpstreamActivityDefaultPolicy(t *testing.T) {
	policy := DefaultUpstreamActivityPolicy()
	if policy.Enabled {
		t.Fatal("default policy must leave reminders disabled")
	}
	if policy.IntervalUnit != UpstreamActivityUnitDays || policy.IntervalValue != 30 {
		t.Fatalf("default interval = %s/%d, want days/30", policy.IntervalUnit, policy.IntervalValue)
	}
	if policy.MinWatchSeconds != 60 {
		t.Fatalf("default minWatchSeconds = %d, want 60", policy.MinWatchSeconds)
	}
	if policy.ManualReferenceAt != nil || policy.Revision != 0 {
		t.Fatalf("default policy must have no manual reference and revision 0: %+v", policy)
	}
}

func TestNormalizeUpstreamActivityPolicyValidation(t *testing.T) {
	now := upstreamActivityTestMillis(t, "2024-01-31T12:34:56Z")
	earlier := now - 1000
	future := now + 1
	zero := int64(0)

	valid := []UpstreamActivityPolicy{
		{IntervalUnit: "days", IntervalValue: 1, MinWatchSeconds: 10},
		{IntervalUnit: " Days ", IntervalValue: 3650, MinWatchSeconds: 3600},
		{IntervalUnit: "months", IntervalValue: 1, MinWatchSeconds: 60},
		{IntervalUnit: "MONTHS", IntervalValue: 120, MinWatchSeconds: 60, ManualReferenceAt: &earlier},
		{IntervalUnit: "days", IntervalValue: 30, MinWatchSeconds: 60, ManualReferenceAt: &now},
	}
	for _, policy := range valid {
		normalized, err := NormalizeUpstreamActivityPolicy(policy, now)
		if err != nil {
			t.Fatalf("valid policy rejected: %+v: %v", policy, err)
		}
		if normalized.IntervalUnit != UpstreamActivityUnitDays && normalized.IntervalUnit != UpstreamActivityUnitMonths {
			t.Fatalf("normalized unit not canonical: %q", normalized.IntervalUnit)
		}
	}

	invalid := []UpstreamActivityPolicy{
		{IntervalUnit: "", IntervalValue: 30, MinWatchSeconds: 60},
		{IntervalUnit: "weeks", IntervalValue: 4, MinWatchSeconds: 60},
		{IntervalUnit: "day", IntervalValue: 1, MinWatchSeconds: 60},
		{IntervalUnit: "days", IntervalValue: 0, MinWatchSeconds: 60},
		{IntervalUnit: "days", IntervalValue: 3651, MinWatchSeconds: 60},
		{IntervalUnit: "months", IntervalValue: 0, MinWatchSeconds: 60},
		{IntervalUnit: "months", IntervalValue: 121, MinWatchSeconds: 60},
		{IntervalUnit: "days", IntervalValue: 30, MinWatchSeconds: 9},
		{IntervalUnit: "days", IntervalValue: 30, MinWatchSeconds: 3601},
		{IntervalUnit: "days", IntervalValue: 30, MinWatchSeconds: 60, ManualReferenceAt: &future},
		{IntervalUnit: "days", IntervalValue: 30, MinWatchSeconds: 60, ManualReferenceAt: &zero},
	}
	for _, policy := range invalid {
		if _, err := NormalizeUpstreamActivityPolicy(policy, now); !errors.Is(err, ErrUpstreamActivityInvalid) {
			t.Fatalf("invalid policy accepted or wrong error: %+v: %v", policy, err)
		}
	}
}

func TestAddUpstreamActivityIntervalDays(t *testing.T) {
	reference := upstreamActivityTestMillis(t, "2024-01-01T00:00:00Z")
	for _, days := range []int{1, 30, 3650} {
		due, err := AddUpstreamActivityInterval(reference, UpstreamActivityUnitDays, days)
		if err != nil {
			t.Fatalf("add %d days: %v", days, err)
		}
		want := reference + int64(days)*upstreamActivityTestDayMillis
		if due != want {
			t.Fatalf("add %d days = %d, want %d", days, due, want)
		}
	}
	if _, err := AddUpstreamActivityInterval(0, UpstreamActivityUnitDays, 30); !errors.Is(err, ErrUpstreamActivityInvalid) {
		t.Fatalf("zero reference accepted: %v", err)
	}
	if _, err := AddUpstreamActivityInterval(reference, "weeks", 1); !errors.Is(err, ErrUpstreamActivityInvalid) {
		t.Fatalf("unknown unit accepted: %v", err)
	}
	if _, err := AddUpstreamActivityInterval(reference, UpstreamActivityUnitMonths, 0); !errors.Is(err, ErrUpstreamActivityInvalid) {
		t.Fatalf("zero months accepted: %v", err)
	}
}

func TestAddUpstreamActivityIntervalMonthEndUTC(t *testing.T) {
	cases := []struct{ reference, want string }{
		// Month-end clamping: never time.AddDate's Jan 31 -> Mar 2/3 behavior.
		{"2023-01-31T12:34:56Z", "2023-02-28T12:34:56Z"},
		{"2024-01-31T12:34:56Z", "2024-02-29T12:34:56Z"},
		{"2024-01-30T08:00:00Z", "2024-02-29T08:00:00Z"},
		{"2024-03-31T23:59:59Z", "2024-04-30T23:59:59Z"},
		{"2024-05-31T00:00:00Z", "2024-06-30T00:00:00Z"},
		{"2024-02-29T12:00:00Z", "2024-03-29T12:00:00Z"},
	}
	reference := upstreamActivityTestMillis(t, "2023-12-31T12:00:00Z")
	due, err := AddUpstreamActivityInterval(reference, UpstreamActivityUnitMonths, 2)
	if err != nil {
		t.Fatalf("add 2 months: %v", err)
	}
	if want := upstreamActivityTestMillis(t, "2024-02-29T12:00:00Z"); due != want {
		t.Fatalf("Dec 31 2023 + 2 months = %s, want 2024-02-29",
			time.UnixMilli(due).UTC().Format(time.RFC3339))
	}

	for _, tc := range cases {
		ref := upstreamActivityTestMillis(t, tc.reference)
		due, err := AddUpstreamActivityInterval(ref, UpstreamActivityUnitMonths, 1)
		if err != nil {
			t.Fatalf("%s + 1 month: %v", tc.reference, err)
		}
		want := upstreamActivityTestMillis(t, tc.want)
		if due != want {
			t.Fatalf("%s + 1 month = %s, want %s", tc.reference,
				time.UnixMilli(due).UTC().Format(time.RFC3339), tc.want)
		}
	}

	ref := upstreamActivityTestMillis(t, "2024-01-31T12:34:56Z")
	due, err = AddUpstreamActivityInterval(ref, UpstreamActivityUnitMonths, 13)
	if err != nil {
		t.Fatalf("add 13 months: %v", err)
	}
	if want := upstreamActivityTestMillis(t, "2025-02-28T12:34:56Z"); due != want {
		t.Fatalf("Jan 31 2024 + 13 months = %s, want 2025-02-28",
			time.UnixMilli(due).UTC().Format(time.RFC3339))
	}

	ref = upstreamActivityTestMillis(t, "2024-02-29T12:00:00Z")
	due, err = AddUpstreamActivityInterval(ref, UpstreamActivityUnitMonths, 1)
	if err != nil {
		t.Fatalf("leap day + 1 month: %v", err)
	}
	if want := upstreamActivityTestMillis(t, "2024-03-29T12:00:00Z"); due != want {
		t.Fatalf("Feb 29 2024 + 1 month = %s, want 2024-03-29",
			time.UnixMilli(due).UTC().Format(time.RFC3339))
	}
}

func TestUpstreamActivityReferenceAt(t *testing.T) {
	older := int64(1_700_000_000_000)
	newer := older + 1000
	zero := int64(0)

	if reference, source := UpstreamActivityReferenceAt(nil, nil); reference != nil || source != "" {
		t.Fatalf("no references must stay unknown: %v/%q", reference, source)
	}
	if reference, source := UpstreamActivityReferenceAt(&older, nil); reference == nil || *reference != older || source != UpstreamActivityReferenceObserved {
		t.Fatalf("observed reference = %v/%q", reference, source)
	}
	if reference, source := UpstreamActivityReferenceAt(nil, &older); reference == nil || *reference != older || source != UpstreamActivityReferenceManual {
		t.Fatalf("manual reference = %v/%q", reference, source)
	}
	if reference, source := UpstreamActivityReferenceAt(&older, &newer); reference == nil || *reference != newer || source != UpstreamActivityReferenceManual {
		t.Fatalf("newer manual must win: %v/%q", reference, source)
	}
	if reference, source := UpstreamActivityReferenceAt(&newer, &older); reference == nil || *reference != newer || source != UpstreamActivityReferenceObserved {
		t.Fatalf("older manual must lose: %v/%q", reference, source)
	}
	if reference, source := UpstreamActivityReferenceAt(&older, &older); reference == nil || *reference != older || source != UpstreamActivityReferenceObserved {
		t.Fatalf("equal manual is not newer and keeps observed label: %v/%q", reference, source)
	}
	if reference, source := UpstreamActivityReferenceAt(&zero, &zero); reference != nil || source != "" {
		t.Fatalf("zero timestamps are not evidence: %v/%q", reference, source)
	}
}

func TestComputeUpstreamActivityViewThresholdBoundaries(t *testing.T) {
	reference := upstreamActivityTestMillis(t, "2024-01-01T00:00:00Z")
	policy := upstreamActivityTestPolicy()
	summary := UpstreamActivitySummary{LastQualifiedAt: &reference}

	cases := []struct {
		name  string
		now   int64
		state string
	}{
		{"fresh", reference, UpstreamActivityStateNormal},
		{"just before half", reference + 15*upstreamActivityTestDayMillis - 1, UpstreamActivityStateNormal},
		{"exactly half", reference + 15*upstreamActivityTestDayMillis, UpstreamActivityStateWarning},
		{"just after half", reference + 15*upstreamActivityTestDayMillis + 1, UpstreamActivityStateWarning},
		{"just before quarter", reference + 22*upstreamActivityTestDayMillis + 12*60*60*1000 - 1, UpstreamActivityStateWarning},
		{"exactly quarter", reference + 22*upstreamActivityTestDayMillis + 12*60*60*1000, UpstreamActivityStateUrgent},
		{"just after quarter", reference + 22*upstreamActivityTestDayMillis + 12*60*60*1000 + 1, UpstreamActivityStateUrgent},
		{"just before due", reference + 30*upstreamActivityTestDayMillis - 1, UpstreamActivityStateUrgent},
		{"exactly due", reference + 30*upstreamActivityTestDayMillis, UpstreamActivityStateOverdue},
		{"past due", reference + 60*upstreamActivityTestDayMillis, UpstreamActivityStateOverdue},
	}
	for _, tc := range cases {
		view := ComputeUpstreamActivityView(policy, summary, tc.now, true)
		if view.State != tc.state {
			t.Fatalf("%s: state = %s, want %s (ratio %v)", tc.name, view.State, tc.state, view.RemainingRatio)
		}
		if view.DueAt == nil || *view.DueAt != reference+30*upstreamActivityTestDayMillis {
			t.Fatalf("%s: dueAt = %v", tc.name, view.DueAt)
		}
	}
}

func TestComputeUpstreamActivityViewStates(t *testing.T) {
	now := upstreamActivityTestMillis(t, "2024-06-01T00:00:00Z")
	observed := now - 10*upstreamActivityTestDayMillis
	older := observed - 1000
	future := now + 1
	policy := upstreamActivityTestPolicy()

	// Unavailable wins over everything and is never green.
	unavailable := ComputeUpstreamActivityView(policy, UpstreamActivitySummary{LastQualifiedAt: &observed}, now, false)
	if unavailable.Available || unavailable.State != UpstreamActivityStateUnavailable ||
		unavailable.Reason != UpstreamActivityReasonStoreUnavailable {
		t.Fatalf("unavailable view = %+v", unavailable)
	}

	// Disabled keeps the observed times visible but never participates in risk.
	disabledPolicy := policy
	disabledPolicy.Enabled = false
	disabled := ComputeUpstreamActivityView(disabledPolicy, UpstreamActivitySummary{
		LastActivityAt: &observed, LastQualifiedAt: &observed,
	}, now, true)
	if disabled.State != UpstreamActivityStateDisabled || disabled.LastActivityAt == nil || *disabled.LastActivityAt != observed {
		t.Fatalf("disabled view = %+v", disabled)
	}
	if disabled.ReferenceAt == nil || disabled.DueAt == nil || disabled.RemainingRatio == nil {
		t.Fatalf("disabled view should still expose schedule evidence: %+v", disabled)
	}

	// No reference at all is unknown, never green and never scheduled.
	unknown := ComputeUpstreamActivityView(policy, UpstreamActivitySummary{}, now, true)
	if unknown.State != UpstreamActivityStateUnknown || unknown.Reason != UpstreamActivityReasonNoReference ||
		unknown.ReferenceAt != nil || unknown.DueAt != nil || unknown.RemainingRatio != nil {
		t.Fatalf("unknown view = %+v", unknown)
	}

	// A reference in the future means the clock moved backwards: still unknown.
	futureView := ComputeUpstreamActivityView(policy, UpstreamActivitySummary{LastQualifiedAt: &future}, now, true)
	if futureView.State != UpstreamActivityStateUnknown || futureView.Reason != UpstreamActivityReasonReferenceInFuture ||
		futureView.DueAt != nil || futureView.RemainingRatio != nil {
		t.Fatalf("future reference view = %+v", futureView)
	}

	// Stored garbage interval fails closed.
	invalidPolicy := policy
	invalidPolicy.IntervalValue = 0
	invalid := ComputeUpstreamActivityView(invalidPolicy, UpstreamActivitySummary{LastQualifiedAt: &observed}, now, true)
	if invalid.State != UpstreamActivityStateUnknown || invalid.Reason != UpstreamActivityReasonInvalidInterval || invalid.DueAt != nil {
		t.Fatalf("invalid interval view = %+v", invalid)
	}

	// Manual reference newer than observed is the baseline and labelled manual.
	manual := now - 2*upstreamActivityTestDayMillis
	manualView := ComputeUpstreamActivityView(policy, UpstreamActivitySummary{LastQualifiedAt: &older}, now, true)
	manualView = ComputeUpstreamActivityView(func() UpstreamActivityPolicy {
		p := policy
		p.ManualReferenceAt = &manual
		return p
	}(), UpstreamActivitySummary{LastQualifiedAt: &older}, now, true)
	if manualView.ReferenceAt == nil || *manualView.ReferenceAt != manual ||
		manualView.ReferenceSource != UpstreamActivityReferenceManual {
		t.Fatalf("manual baseline view = %+v", manualView)
	}
	if manualView.LastQualifiedAt == nil || *manualView.LastQualifiedAt != older || manualView.ManualReferenceAt == nil {
		t.Fatalf("observed and manual evidence must stay separate: %+v", manualView)
	}

	// Cycle labels are never ambiguous: manual older than observed keeps observed.
	olderManual := older - 1000
	observedWins := ComputeUpstreamActivityView(func() UpstreamActivityPolicy {
		p := policy
		p.ManualReferenceAt = &olderManual
		return p
	}(), UpstreamActivitySummary{LastQualifiedAt: &older}, now, true)
	if observedWins.ReferenceSource != UpstreamActivityReferenceObserved || *observedWins.ReferenceAt != older {
		t.Fatalf("older manual must not override observed: %+v", observedWins)
	}

	for name, view := range map[string]UpstreamActivityView{
		"unavailable": unavailable, "unknown": unknown, "future": futureView, "invalid": invalid,
	} {
		if view.State == UpstreamActivityStateNormal {
			t.Fatalf("%s must never render normal/green", name)
		}
	}
}

func TestComputeUpstreamActivityViewMonthsUTC(t *testing.T) {
	reference := upstreamActivityTestMillis(t, "2024-01-31T12:34:56Z")
	policy := upstreamActivityTestPolicy()
	policy.IntervalUnit = UpstreamActivityUnitMonths
	policy.IntervalValue = 1

	view := ComputeUpstreamActivityView(policy, UpstreamActivitySummary{LastQualifiedAt: &reference}, reference, true)
	if view.DueAt == nil {
		t.Fatalf("monthly view missing dueAt: %+v", view)
	}
	wantDue := upstreamActivityTestMillis(t, "2024-02-29T12:34:56Z")
	if *view.DueAt != wantDue {
		t.Fatalf("monthly dueAt = %s, want 2024-02-29T12:34:56Z",
			time.UnixMilli(*view.DueAt).UTC().Format(time.RFC3339))
	}
	if view.RemainingRatio == nil || *view.RemainingRatio != 1 {
		t.Fatalf("fresh monthly ratio = %v, want 1", view.RemainingRatio)
	}
}

func TestUpstreamActivityTimeRFC3339RoundTrip(t *testing.T) {
	if got := UpstreamActivityTimeRFC3339(nil); got != nil {
		t.Fatalf("nil timestamp rendered as %v", *got)
	}
	zero := int64(0)
	if got := UpstreamActivityTimeRFC3339(&zero); got != nil {
		t.Fatalf("zero timestamp rendered as 1970: %v", *got)
	}
	millis := upstreamActivityTestMillis(t, "2024-01-31T12:34:56Z")
	formatted := UpstreamActivityTimeRFC3339(&millis)
	if formatted == nil || *formatted != "2024-01-31T12:34:56Z" {
		t.Fatalf("formatted = %v", formatted)
	}
	parsed, err := ParseUpstreamActivityTimeRFC3339(*formatted)
	if err != nil || parsed == nil || *parsed != millis {
		t.Fatalf("round trip = %v, %v", parsed, err)
	}
	if _, err := ParseUpstreamActivityTimeRFC3339("31/01/2024"); !errors.Is(err, ErrUpstreamActivityInvalid) {
		t.Fatalf("invalid timestamp accepted: %v", err)
	}
}

func TestUpstreamActivityViewNeverSerializesAccountScope(t *testing.T) {
	reference := upstreamActivityTestMillis(t, "2024-01-31T12:34:56Z")
	secretScope := "v1:0123456789abcdef-secret-internal-scope"
	policy := upstreamActivityTestPolicy()
	policy.ManualScope = secretScope
	summary := UpstreamActivitySummary{
		ServerID:        "server-1",
		AccountScope:    secretScope,
		LastActivityAt:  &reference,
		LastQualifiedAt: &reference,
	}
	view := ComputeUpstreamActivityView(policy, summary, reference, true)
	payload, err := json.Marshal(struct {
		Policy  UpstreamActivityPolicy  `json:"policy"`
		Summary UpstreamActivitySummary `json:"summary"`
		View    UpstreamActivityView    `json:"view"`
	}{policy, summary, view})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	lower := strings.ToLower(string(payload))
	if strings.Contains(lower, "scope") || strings.Contains(lower, "0123456789abcdef") {
		t.Fatalf("serialized activity leaked the internal account scope: %s", payload)
	}
}
