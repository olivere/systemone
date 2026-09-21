package systemone

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// ShadowConfig compares a candidate with the primary without waiting for it.
type ShadowConfig struct {
	Primary       Backend
	Candidate     Backend
	Timeout       time.Duration
	MaxConcurrent int // Zero defaults to one; includes reporting.
	// Report may be called concurrently and must honor context cancellation.
	// It receives snapshots and may retain or modify them. Nothing is logged.
	// Its context shares the candidate deadline and may already be cancelled.
	Report func(context.Context, Comparison)
}

// Comparison contains the same request evaluated by two backends. Err describes
// candidate failure, including unsupported requests and invalid responses.
type Comparison struct {
	Request   Request
	Primary   Response
	Candidate Response
	Err       error
}

// Shadow owns a bounded worker pool. Close it when the application shuts down.
// Closing stops comparisons; primary evaluations remain available afterward.
type Shadow struct {
	primary   Backend
	candidate Backend
	report    func(context.Context, Comparison)
	timeout   time.Duration
	cancel    context.CancelFunc
	done      <-chan struct{}
	jobs      chan Comparison
	slots     chan struct{}
	workers   sync.WaitGroup
	mu        sync.Mutex
	closed    bool
	dropped   atomic.Uint64
}

// NewShadow roots candidate work in the application lifetime, independently of
// individual request cancellation. Timeout must be positive.
func NewShadow(lifetime context.Context, cfg ShadowConfig) (*Shadow, error) {
	if isNil(lifetime) || isNil(cfg.Primary) || isNil(cfg.Candidate) || cfg.Report == nil {
		return nil, errors.New("systemone/shadow: lifetime, backends, and report are required")
	}
	if cfg.Timeout <= 0 || cfg.MaxConcurrent < 0 {
		return nil, errors.New("systemone/shadow: timeout must be positive and concurrency nonnegative")
	}
	n := max(1, cfg.MaxConcurrent)
	ctx, cancel := context.WithCancel(lifetime)
	s := &Shadow{
		primary: cfg.Primary, candidate: cfg.Candidate, report: cfg.Report,
		timeout: cfg.Timeout, cancel: cancel, done: ctx.Done(),
		jobs: make(chan Comparison, n), slots: make(chan struct{}, n),
	}
	for range n {
		s.workers.Go(func() { s.run(ctx) })
	}
	return s, nil
}

func (s *Shadow) Capabilities() Capabilities { return s.primary.Capabilities() }

func (s *Shadow) Evaluate(ctx context.Context, req Request) (Response, error) {
	if err := ctx.Err(); err != nil {
		return Response{}, err
	}
	if err := CheckRequest(req, s.Capabilities()); err != nil {
		return Response{}, err
	}
	snapshot := cloneRequest(req)
	res, err := s.primary.Evaluate(ctx, cloneRequest(snapshot))
	if err != nil {
		return Response{}, err
	}
	if err := ctx.Err(); err != nil {
		return Response{}, err
	}
	if err := ValidateResponse(snapshot, res); err != nil {
		return Response{}, err
	}
	s.submit(Comparison{Request: snapshot, Primary: cloneResponse(res)})
	return res, nil
}

func (s *Shadow) submit(job Comparison) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		s.dropped.Add(1)
		return
	}
	select {
	case <-s.done:
		s.dropped.Add(1)
		return
	default:
	}
	select {
	case s.slots <- struct{}{}:
		s.jobs <- job // A reserved slot guarantees capacity.
	default:
		s.dropped.Add(1)
	}
}

func (s *Shadow) run(lifetime context.Context) {
	for {
		select {
		case <-lifetime.Done():
			return
		case job := <-s.jobs:
			if lifetime.Err() != nil {
				s.dropped.Add(1)
				<-s.slots
				return
			}
			s.compare(lifetime, job)
			<-s.slots
		}
	}
}

func (s *Shadow) compare(lifetime context.Context, job Comparison) {
	ctx, cancel := context.WithTimeout(lifetime, s.timeout)
	defer cancel()
	job.Err = CheckRequest(job.Request, s.candidate.Capabilities())
	if job.Err == nil {
		job.Candidate, job.Err = s.candidate.Evaluate(ctx, cloneRequest(job.Request))
		if job.Err == nil {
			job.Err = ctx.Err()
		}
		if job.Err == nil {
			job.Err = ValidateResponse(job.Request, job.Candidate)
		}
	}
	if job.Err != nil {
		job.Err = fmt.Errorf("systemone/shadow: candidate: %w", job.Err)
	}
	job.Candidate = cloneResponse(job.Candidate)
	s.report(ctx, job)
}

// Dropped counts successful primary evaluations omitted because all workers
// were occupied or shadow comparisons had stopped.
func (s *Shadow) Dropped() uint64 { return s.dropped.Load() }

// Close cancels candidate work and waits for workers and reports to return.
// Backends and Report must honor cancellation or Close can block indefinitely.
// Report must not call Close itself. Close is safe to call concurrently.
func (s *Shadow) Close() {
	s.mu.Lock()
	s.closed = true
	s.cancel()
	s.mu.Unlock()
	s.workers.Wait()
	// No worker or submission can race this drain. Release queued input data
	// even when callers retain the wrapper to keep using its primary backend.
	s.mu.Lock()
	defer s.mu.Unlock()
	for {
		select {
		case <-s.jobs:
			<-s.slots
			s.dropped.Add(1)
		default:
			return
		}
	}
}
