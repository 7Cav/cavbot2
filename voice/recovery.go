package voice

import (
	"slices"
	"sync"
	"time"
)

// stepsKept is how many recovery steps a gap's report lists, the last
// ones. It counts those before them.
const stepsKept = 20

// recoveryLog is the recovery steps one voice connection has taken, kept
// from the start of the gap its receiver last told of, for the gap's
// report. Each step is noted where the recorder takes it. Past stepsKept,
// the oldest step that ended gives way, and earlier counts it. A step still
// under way stays.
type recoveryLog struct {
	mu      sync.Mutex
	steps   []*recoveryStep
	earlier int
}

// recoveryStep is one step in the log, under way until it ends.
type recoveryStep struct {
	kind      StepKind
	token     DialToken
	at        time.Time
	outcome   StepOutcome
	closeCode string
}

// start notes a step of kind starting now, with token for a dial, and
// returns it, under way until end.
func (l *recoveryLog) start(kind StepKind, token DialToken) *recoveryStep {
	s := &recoveryStep{kind: kind, token: token, at: time.Now(), outcome: StepUnderWay}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.steps = append(l.steps, s)
	if len(l.steps) > stepsKept {
		if i := slices.IndexFunc(l.steps, (*recoveryStep).ended); i != -1 {
			l.steps = slices.Delete(l.steps, i, i+1)
			l.earlier++
		}
	}
	return s
}

// ended reports whether the step has ended. The log's mutex guards it.
func (s *recoveryStep) ended() bool { return s.outcome != StepUnderWay }

// end notes s ending with outcome, by err. A step a voice gateway close
// ended keeps the close's code.
func (l *recoveryLog) end(s *recoveryStep, outcome StepOutcome, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	s.outcome = outcome
	if code := closeCode(err); code != CloseCodeNone {
		s.closeCode = code
	}
}

// failed notes a step of kind that failed now, by err.
func (l *recoveryLog) failed(kind StepKind, err error) {
	l.end(l.start(kind, ""), StepFailed, err)
}

// from forgets the steps that started before at, the start of a gap, so
// the log holds that gap's steps alone.
func (l *recoveryLog) from(at time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.steps = slices.DeleteFunc(l.steps, func(s *recoveryStep) bool { return s.at.Before(at) })
	l.earlier = 0
}

// report is the steps in the log, each timed from since, the gap's start:
// those that ended, in the order they started, then those still under way,
// so the last says what was still under way. It returns the count of the
// steps before them too.
func (l *recoveryLog) report(since time.Time) (steps []RecoveryStep, earlier int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var underWay []RecoveryStep
	for _, s := range l.steps {
		step := RecoveryStep{Kind: s.kind, Token: s.token, At: s.at.Sub(since), Outcome: s.outcome, CloseCode: s.closeCode}
		if s.ended() {
			steps = append(steps, step)
		} else {
			underWay = append(underWay, step)
		}
	}
	return append(steps, underWay...), l.earlier
}

// outcomeOf is the outcome of a step that succeeded when ok, and failed
// otherwise.
func outcomeOf(ok bool) StepOutcome {
	if ok {
		return StepSucceeded
	}
	return StepFailed
}
