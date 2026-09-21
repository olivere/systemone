package systemone

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

type shadowBackend struct {
	caps Capabilities
	eval func(context.Context, Request) (Response, error)
}

func (b shadowBackend) Capabilities() Capabilities { return b.caps }
func (b shadowBackend) Evaluate(ctx context.Context, req Request) (Response, error) {
	return b.eval(ctx, req)
}

func shadowRequest() Request {
	return Request{State: []byte(`"ticket"`), Questions: []QuestionSpec{{Key: "refund", Kind: NoulKind, Instructions: "Wants a refund?"}}}
}

func shadowResponse() Response {
	return Response{Answers: map[string]Answer{"refund": {Noul: &NoulAnswer{Probability: 0.7}}}}
}

func shadowPrimary() shadowBackend {
	return shadowBackend{caps: Capabilities{Noul: true}, eval: func(context.Context, Request) (Response, error) {
		return shadowResponse(), nil
	}}
}

func receiveShadow[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for shadow work")
		var zero T
		return zero
	}
}

func TestShadowIndependentAndBounded(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	release := make(chan struct{})
	reports := make(chan Comparison, 1)
	candidate := shadowPrimary()
	candidate.eval = func(ctx context.Context, req Request) (Response, error) {
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
			return Response{}, ctx.Err()
		}
		if string(req.State) != `"ticket"` || req.Questions[0].Instructions != "Wants a refund?" {
			t.Error("candidate request was mutated")
		}
		return shadowResponse(), nil
	}
	s, err := NewShadow(t.Context(), ShadowConfig{
		Primary: shadowPrimary(), Candidate: candidate, Timeout: time.Minute,
		Report: func(_ context.Context, c Comparison) { reports <- c },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	req := shadowRequest()
	ctx, cancel := context.WithCancel(t.Context())
	res, err := s.Evaluate(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	receiveShadow(t, started)
	cancel()
	req.State[1] = 'X'
	req.Questions[0].Instructions = "changed"
	res.Answers["refund"].Noul.Probability = 0.1
	if _, err := s.Evaluate(t.Context(), shadowRequest()); err != nil {
		t.Fatal(err)
	}
	if s.Dropped() != 1 {
		t.Fatalf("dropped = %d, want 1", s.Dropped())
	}
	close(release)
	report := receiveShadow(t, reports)
	if report.Err != nil || report.Primary.Answers["refund"].Noul.Probability != 0.7 {
		t.Fatalf("comparison = %+v", report)
	}
}

func TestShadowCandidateFailures(t *testing.T) {
	t.Parallel()
	failure := errors.New("candidate unavailable")
	for _, tt := range []struct {
		name      string
		candidate shadowBackend
		want      error
	}{
		{"backend", shadowBackend{Capabilities{Noul: true}, func(context.Context, Request) (Response, error) { return Response{}, failure }}, failure},
		{"malformed", shadowBackend{Capabilities{Noul: true}, func(context.Context, Request) (Response, error) { return Response{}, nil }}, ErrInvalidResponse},
		{"unsupported", shadowBackend{Capabilities{}, func(context.Context, Request) (Response, error) {
			t.Error("unsupported candidate called")
			return Response{}, nil
		}}, ErrUnsupported},
	} {
		t.Run(tt.name, func(t *testing.T) {
			reports := make(chan Comparison, 1)
			s, err := NewShadow(t.Context(), ShadowConfig{Primary: shadowPrimary(), Candidate: tt.candidate, Timeout: time.Minute, Report: func(_ context.Context, c Comparison) { reports <- c }})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(s.Close)
			if _, err := s.Evaluate(t.Context(), shadowRequest()); err != nil {
				t.Fatal(err)
			}
			if report := receiveShadow(t, reports); !errors.Is(report.Err, tt.want) {
				t.Fatalf("error = %v, want %v", report.Err, tt.want)
			}
		})
	}
}

func TestShadowPrimaryFailure(t *testing.T) {
	t.Parallel()
	for _, malformed := range []bool{false, true} {
		primary := shadowPrimary()
		primary.eval = func(context.Context, Request) (Response, error) {
			if malformed {
				return Response{}, nil
			}
			return Response{}, errors.New("primary unavailable")
		}
		var calls atomic.Int64
		candidate := shadowPrimary()
		candidate.eval = func(context.Context, Request) (Response, error) { calls.Add(1); return shadowResponse(), nil }
		s, err := NewShadow(t.Context(), ShadowConfig{Primary: primary, Candidate: candidate, Timeout: time.Minute, Report: func(context.Context, Comparison) { calls.Add(1) }})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Evaluate(t.Context(), shadowRequest()); err == nil {
			t.Error("expected primary error")
		}
		s.Close()
		if calls.Load() != 0 {
			t.Fatal("candidate ran after primary failure")
		}
	}
}

func TestShadowShutdown(t *testing.T) {
	t.Parallel()
	for _, lifetimeCancel := range []bool{false, true} {
		ctx, cancel := context.WithCancel(t.Context())
		started := make(chan struct{})
		reports := make(chan Comparison, 1)
		candidate := shadowPrimary()
		candidate.eval = func(ctx context.Context, _ Request) (Response, error) {
			close(started)
			<-ctx.Done()
			return Response{}, ctx.Err()
		}
		s, err := NewShadow(ctx, ShadowConfig{Primary: shadowPrimary(), Candidate: candidate, Timeout: time.Minute, Report: func(_ context.Context, c Comparison) { reports <- c }})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Evaluate(t.Context(), shadowRequest()); err != nil {
			t.Fatal(err)
		}
		receiveShadow(t, started)
		if lifetimeCancel {
			cancel()
		} else {
			s.Close()
		}
		if report := receiveShadow(t, reports); !errors.Is(report.Err, context.Canceled) {
			t.Fatalf("error = %v", report.Err)
		}
		var wg sync.WaitGroup
		for range 20 {
			wg.Go(func() { s.Close() })
			wg.Go(func() {
				if _, err := s.Evaluate(t.Context(), shadowRequest()); err != nil {
					t.Error(err)
				}
			})
		}
		wg.Wait()
		cancel()
		if s.Dropped() != 20 {
			t.Fatalf("dropped = %d, want 20", s.Dropped())
		}
	}
}

func TestShadowConfig(t *testing.T) {
	t.Parallel()
	valid := ShadowConfig{Primary: shadowPrimary(), Candidate: shadowPrimary(), Timeout: time.Minute, Report: func(context.Context, Comparison) {}}
	for _, change := range []func(*ShadowConfig){
		func(c *ShadowConfig) { c.Primary = nil },
		func(c *ShadowConfig) { c.Primary = (*shadowBackend)(nil) },
		func(c *ShadowConfig) { c.Candidate = nil },
		func(c *ShadowConfig) { c.Candidate = (*shadowBackend)(nil) },
		func(c *ShadowConfig) { c.Timeout = 0 },
		func(c *ShadowConfig) { c.MaxConcurrent = -1 },
		func(c *ShadowConfig) { c.Report = nil },
	} {
		cfg := valid
		change(&cfg)
		if s, err := NewShadow(t.Context(), cfg); err == nil {
			s.Close()
			t.Error("expected invalid config error")
		}
	}
	//lint:ignore SA1012 The constructor must reject a nil application lifetime.
	if s, err := NewShadow(nil, valid); err == nil {
		s.Close()
		t.Error("expected nil lifetime error")
	}
}

func TestShadowTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		candidate := shadowPrimary()
		candidate.eval = func(ctx context.Context, _ Request) (Response, error) {
			<-ctx.Done()
			return Response{}, ctx.Err()
		}
		reports := make(chan Comparison, 1)
		s, err := NewShadow(t.Context(), ShadowConfig{
			Primary: shadowPrimary(), Candidate: candidate, Timeout: time.Second,
			Report: func(_ context.Context, c Comparison) { reports <- c },
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(s.Close)
		if _, err := s.Evaluate(t.Context(), shadowRequest()); err != nil {
			t.Fatal(err)
		}
		if report := receiveShadow(t, reports); !errors.Is(report.Err, context.DeadlineExceeded) {
			t.Fatalf("error = %v, want deadline exceeded", report.Err)
		}
	})
}

func TestShadowConcurrentCloseAndEvaluate(t *testing.T) {
	t.Parallel()
	var reports atomic.Int64
	s, err := NewShadow(t.Context(), ShadowConfig{
		Primary: shadowPrimary(), Candidate: shadowPrimary(), Timeout: time.Minute, MaxConcurrent: 4,
		Report: func(context.Context, Comparison) { reports.Add(1) },
	})
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() {
			<-start
			if _, err := s.Evaluate(t.Context(), shadowRequest()); err != nil {
				t.Error(err)
			}
		})
	}
	for range 4 {
		wg.Go(func() { <-start; s.Close() })
	}
	close(start)
	wg.Wait()
	s.Close()
	if s.Dropped()+uint64(reports.Load()) != 50 {
		t.Fatalf("unaccounted comparisons: %d dropped, %d reported", s.Dropped(), reports.Load())
	}
	if len(s.jobs) != 0 || len(s.slots) != 0 {
		t.Fatal("shutdown retained queued requests or reserved slots")
	}
}
