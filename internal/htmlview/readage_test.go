package htmlview

import (
	"strings"
	"testing"
	"time"
)

// The busy fixture's read.
var busyRead = time.Date(2030, 6, 15, 12, 0, 0, 0, time.UTC)

func ageOf(t *testing.T, opts Options) []ageStep {
	t.Helper()
	p, err := build(encode(t, busy(t)), opts)
	if err != nil {
		t.Fatal(err)
	}
	return p.Map.ReadAge
}

// stepAt is the step a page shows once the read is after old, the latest whose
// moment has passed.
func stepAt(t *testing.T, steps []ageStep, after time.Duration) ageStep {
	t.Helper()
	now := busyRead.Add(after)
	var shown *ageStep
	for i := range steps {
		at, err := time.Parse(time.RFC3339, steps[i].At)
		if err != nil {
			t.Fatalf("step %d has moment %q: %v", i, steps[i].At, err)
		}
		if at.After(now) {
			break
		}
		shown = &steps[i]
	}
	if shown == nil {
		t.Fatalf("no step has passed %s after the read", after)
	}
	return *shown
}

func TestTheReadAgeStartsAtTheReadItself(t *testing.T) {
	steps := ageOf(t, Options{})
	if got := steps[0].At; got != "2030-06-15T12:00:00Z" {
		t.Errorf("first step at %s, want the read's own moment", got)
	}
	if got := stepAt(t, steps, 30*time.Second).Text; got != "read under a minute ago" {
		t.Errorf("half a minute in reads %q", got)
	}
}

func TestTheReadAgeCountsInTheLargestWholeUnitAsLookoutShowDoes(t *testing.T) {
	steps := ageOf(t, Options{})
	for _, c := range []struct {
		after time.Duration
		want  string
	}{
		{5*time.Minute + 30*time.Second, "read 5m ago"},
		{59 * time.Minute, "read 59m ago"},
		{90 * time.Minute, "read 1h ago"},
		{30 * time.Hour, "read 1d ago"},
		{5 * 24 * time.Hour, "read 5d ago"},
	} {
		if got := stepAt(t, steps, c.after).Text; !strings.HasPrefix(got, c.want) {
			t.Errorf("%s after the read shows %q, want it to start %q", c.after, got, c.want)
		}
	}
}

func TestTheReadTurnsOldAtTwoHoursByDefault(t *testing.T) {
	steps := ageOf(t, Options{})
	if s := stepAt(t, steps, 2*time.Hour-time.Second); s.Old {
		t.Errorf("a second before two hours is already old: %+v", s)
	}
	s := stepAt(t, steps, 2*time.Hour)
	if !s.Old {
		t.Errorf("two hours after the read is not old: %+v", s)
	}
	if want := "read 2h ago; run lookout view again, then reload, to see the latest"; s.Text != want {
		t.Errorf("an old read says %q, want %q", s.Text, want)
	}
	if s := stepAt(t, steps, 9*24*time.Hour); !s.Old {
		t.Errorf("an old read stops being old later: %+v", s)
	}
}

func TestAThresholdBetweenStepsTurnsOldExactlyThere(t *testing.T) {
	steps := ageOf(t, Options{ReadOldAfter: 90 * time.Minute})
	if s := stepAt(t, steps, 90*time.Minute-time.Second); s.Old {
		t.Errorf("a second before the threshold is already old: %+v", s)
	}
	if s := stepAt(t, steps, 90*time.Minute); !s.Old || !strings.HasPrefix(s.Text, "read 1h ago;") {
		t.Errorf("at the threshold: %+v, want old and reading 1h", s)
	}
}

func TestTheStepsRunInOrderAndStayFew(t *testing.T) {
	steps := ageOf(t, Options{})
	for i := 1; i < len(steps); i++ {
		if steps[i].At <= steps[i-1].At {
			t.Fatalf("step %d at %s does not follow %s", i, steps[i].At, steps[i-1].At)
		}
	}
	// The page carries every step inline, so the schedule thins out as the
	// read ages rather than ticking each minute for ever.
	if len(steps) > 200 {
		t.Errorf("%d steps; the schedule should stay a few hundred bytes of JSON per step at most", len(steps))
	}
}

func TestThePageCarriesTheScheduleForItsScriptAndShowsNothingWithout(t *testing.T) {
	out := render(t, busy(t))
	if !strings.Contains(out, `"read_age":[{"at":"2030-06-15T12:00:00Z"`) {
		t.Error("the page's data block holds no read-age schedule")
	}
	if !strings.Contains(out, `<span class="readage" role="status" hidden></span>`) {
		t.Error("the page holds no hidden read-age slot for its script to fill")
	}
}
