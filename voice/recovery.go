package voice

import (
	"slices"
	"sync"
	"time"

	disgovoice "github.com/disgoorg/disgo/voice"
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

// recoveryStep is one step in the log, which started at at, under way
// until it ends. Its At is set when a report times it. A dial's step keeps
// the disgo gateway it dialed through as via.
type recoveryStep struct {
	step RecoveryStep
	at   time.Time
	via  disgovoice.Gateway
}

// start notes a step of kind starting now, with token for a dial, and
// returns it, under way until end.
func (l *recoveryLog) start(kind StepKind, token DialToken) *recoveryStep {
	return l.add(&recoveryStep{step: RecoveryStep{Kind: kind, Token: token, Outcome: StepUnderWay}, at: time.Now()})
}

// dial notes a dial starting now with token, through the disgo gateway via,
// and returns its step, under way until end.
func (l *recoveryLog) dial(token DialToken, via disgovoice.Gateway) *recoveryStep {
	return l.add(&recoveryStep{step: RecoveryStep{Kind: StepDial, Token: token, Outcome: StepUnderWay}, at: time.Now(), via: via})
}

// add notes s, a step starting now, and returns it.
func (l *recoveryLog) add(s *recoveryStep) *recoveryStep {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.steps = append(l.steps, s)
	l.keepLocked()
	return s
}

// keepLocked drops the oldest steps that ended, counting each, until the
// log holds stepsKept steps or every one left is still under way. Caller
// holds mu.
func (l *recoveryLog) keepLocked() {
	for len(l.steps) > stepsKept {
		i := slices.IndexFunc(l.steps, (*recoveryStep).ended)
		if i == -1 {
			return
		}
		l.steps = slices.Delete(l.steps, i, i+1)
		l.earlier++
	}
}

// ended reports whether the step has ended. The log's mutex guards it.
func (s *recoveryStep) ended() bool { return s.step.Outcome != StepUnderWay }

// end notes s ending with outcome, by err. A step a voice gateway close
// ended keeps the close's code.
func (l *recoveryLog) end(s *recoveryStep, outcome StepOutcome, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	s.step.Outcome = outcome
	s.keepCloseCodeLocked(err)
	l.keepLocked()
}

// closedThrough notes err, a close of the disgo gateway gw, as the close
// that ended the last dial through gw, unless that dial succeeded. The
// close can reach the recorder after the dial has ended, and after later
// dials through other gateways have started.
func (l *recoveryLog) closedThrough(gw disgovoice.Gateway, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, s := range slices.Backward(l.steps) {
		if s.via == gw {
			if s.step.Outcome != StepSucceeded {
				s.keepCloseCodeLocked(err)
			}
			return
		}
	}
}

// keepCloseCodeLocked keeps the code of err on s, when err is a voice
// gateway close. A step that ends with no close keeps any code noted on it
// earlier. Caller holds the log's mutex.
func (s *recoveryStep) keepCloseCodeLocked(err error) {
	if code := closeCode(err); code != "" && code != CloseCodeNone {
		s.step.CloseCode = code
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
		step := s.step
		step.At = s.at.Sub(since)
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
