package clock

import "time"

// Clock abstracts time measurement to allow for deterministic testing.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
	Since(t time.Time) time.Duration
}

// RealClock implements Clock using the standard time package.
type RealClock struct{}

func (RealClock) Now() time.Time {
	return time.Now()
}

func (RealClock) After(d time.Duration) <-chan time.Time {
	return time.After(d)
}

func (RealClock) Since(t time.Time) time.Duration {
	return time.Since(t)
}

// MockClock is a controllable clock for testing.
type MockClock struct {
	CurrentTime time.Time
}

func (m *MockClock) Now() time.Time {
	return m.CurrentTime
}

func (m *MockClock) After(d time.Duration) <-chan time.Time {
	// In a real mock, we might want to register a waiter.
	// For simple determinism checks, we might just return closed channel or nil depending on need.
	// Returning a channel that fires immediately is often safe for "fast forward" tests.
	c := make(chan time.Time, 1)
	c <- m.CurrentTime.Add(d)
	return c
}

func (m *MockClock) Since(t time.Time) time.Duration {
	return m.CurrentTime.Sub(t)
}

func (m *MockClock) Advance(d time.Duration) {
	m.CurrentTime = m.CurrentTime.Add(d)
}
