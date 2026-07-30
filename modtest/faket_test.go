package modtest

import "fmt"

// fakeT is a recorder implementing TB, used by modtest's own test suite to
// verify that an Assert* helper correctly reports a failure without that
// failure propagating to (and failing) the outer *testing.T tree.
//
// Using a real *testing.T subtest via t.Run for this would not work: Go's
// testing package always marks a parent test as failed once any of its
// subtests fail, regardless of what the parent does with t.Run's returned
// bool afterward. fakeT sidesteps that by implementing TB independently of
// the standard library's testing.TB (which cannot be implemented outside
// package testing, since it has an unexported method).
type fakeT struct {
	failed   bool
	logs     []string
	cleanups []func()
}

// fatalSignal is panicked by fakeT.Fatalf and recovered by runFake, mimicking
// testing.T.FailNow's control-flow-stopping behavior without calling
// runtime.Goexit — which would terminate the real, enclosing test goroutine
// rather than just the fake call chain.
type fatalSignal struct{}

func (f *fakeT) Helper() {}

func (f *fakeT) Logf(format string, args ...any) {
	f.logs = append(f.logs, fmt.Sprintf(format, args...))
}

func (f *fakeT) Errorf(format string, args ...any) {
	f.failed = true
	f.logs = append(f.logs, fmt.Sprintf(format, args...))
}

func (f *fakeT) Fatalf(format string, args ...any) {
	f.failed = true
	f.logs = append(f.logs, fmt.Sprintf(format, args...))
	panic(fatalSignal{})
}

func (f *fakeT) Cleanup(fn func()) {
	// Assert* helpers that register cleanup (e.g. via lookupCheck) only rely
	// on it running eventually to release resources; running it immediately
	// after fn returns is sufficient for these self-tests. runFake defers
	// draining fakeT.cleanups after fn returns or panics.
	f.cleanups = append(f.cleanups, fn)
}

// runFake calls fn with a fresh fakeT, recovering a Fatalf-induced
// fatalSignal panic (any other panic propagates, since that would be a real
// bug rather than an expected test failure), running any registered
// cleanups, and returning the recorder so the caller can assert on
// ft.failed.
func runFake(fn func(t TB)) *fakeT {
	ft := &fakeT{}
	func() {
		defer func() {
			for i := len(ft.cleanups) - 1; i >= 0; i-- {
				ft.cleanups[i]()
			}
		}()
		defer func() {
			if r := recover(); r != nil {
				if _, ok := r.(fatalSignal); !ok {
					panic(r)
				}
			}
		}()
		fn(ft)
	}()
	return ft
}
