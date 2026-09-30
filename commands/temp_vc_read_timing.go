package commands

import (
	"io"
	"net/http"
	"sync"
	"time"
)

// ReadTiming is how one Discord read spent its time (#395), for a report
// that has to say whether Discord was slow or the read sat inside
// discordgo. Both times are summed over the read's attempts.
type ReadTiming struct {
	// Waiting is the time inside discordgo before the read's requests went
	// out: the route's bucket lock, which another request on the route
	// holds until it finishes, and the sleep for an empty bucket.
	Waiting time.Duration
	// Trips is the time on requests to Discord and back, each to the end of
	// its answer's body.
	Trips time.Duration
	// Attempts is how many requests went out, more than one when discordgo
	// retried a 502.
	Attempts int
}

// readClock times one read's requests through the client it hands
// discordgo: a copy of the session's client, the same timeout and the same
// transport beneath, whose transport notes when each request goes out and
// when its answer's body ends. It carries no context, so it changes nothing
// about when a read gives up.
type readClock struct {
	client *http.Client

	mu sync.Mutex
	// mark is the read's start, then the end of its last trip: where the
	// next wait starts.
	mark time.Time
	// sent is when the current request went out.
	sent  time.Time
	spent ReadTiming
}

// newReadClock starts a read's clock over the session's client.
func newReadClock(session *http.Client) *readClock {
	c := &readClock{mark: time.Now()}
	client := *session
	base := client.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	client.Transport = timedTransport{base: base, clock: c}
	c.client = &client
	return c
}

// timing returns the read's timing so far.
func (c *readClock) timing() ReadTiming {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.spent
}

func (c *readClock) sending() {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	c.spent.Waiting += now.Sub(c.mark)
	c.spent.Attempts++
	c.sent = now
}

func (c *readClock) answered() {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	c.spent.Trips += now.Sub(c.sent)
	c.mark = now
}

// timedTransport is the read clock's transport. A trip ends when its
// request fails, or when discordgo reaches the end of the answer's body,
// fails reading it, or closes it, whichever comes first.
type timedTransport struct {
	base  http.RoundTripper
	clock *readClock
}

func (t timedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.clock.sending()
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		t.clock.answered()
		return nil, err
	}
	resp.Body = &timedBody{ReadCloser: resp.Body, end: sync.OnceFunc(t.clock.answered)}
	return resp, nil
}

type timedBody struct {
	io.ReadCloser
	end func()
}

func (b *timedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil {
		b.end()
	}
	return n, err
}

func (b *timedBody) Close() error {
	b.end()
	return b.ReadCloser.Close()
}
