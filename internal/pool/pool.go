package pool

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"os/exec"
	"sort"
	"sync"
	"syscall"
	"time"

	"github.com/whisper-darkly/sticky-refinery/internal/config"
	"github.com/whisper-darkly/sticky-refinery/internal/executor"
	"github.com/whisper-darkly/sticky-refinery/internal/scanner"
	"github.com/whisper-darkly/sticky-refinery/internal/store"
)

// OnCompleteFunc is called when a job finishes (success or error).
type OnCompleteFunc func(path string, pipelineName string, err error)

// WorkerStatus is a snapshot of a running worker.
type WorkerStatus struct {
	ID        string
	Path      string
	Pipeline  string
	StartedAt time.Time
}

// Worker holds a running conversion command.
type worker struct {
	id        string
	path      string
	pipeline  string
	startedAt time.Time
	cancel    context.CancelFunc
	cmd       *exec.Cmd
}

// Pool manages concurrent conversion jobs.
type Pool struct {
	mu          sync.Mutex
	size        int
	shrinkGrace time.Duration
	killOrder   string // "oldest" or "youngest"
	workers     map[string]*worker
	shrinkTimer *time.Timer

	store      *store.Store
	pipelines  map[string]config.PipelineConfig
	onComplete OnCompleteFunc
}

// New creates a Pool. onComplete is called from a goroutine after each job finishes.
func New(cfg config.PoolConfig, st *store.Store, pipelines []config.PipelineConfig, onComplete OnCompleteFunc) *Pool {
	pm := make(map[string]config.PipelineConfig, len(pipelines))
	for _, p := range pipelines {
		pm[p.Name] = p
	}
	return &Pool{
		size:        cfg.Size,
		shrinkGrace: cfg.ShrinkGrace.Duration,
		killOrder:   cfg.ShrinkKillOrder,
		workers:     make(map[string]*worker),
		store:       st,
		pipelines:   pm,
		onComplete:  onComplete,
	}
}

// ActiveCount returns the number of running workers.
func (p *Pool) ActiveCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.workers)
}

// Size returns the configured pool size.
func (p *Pool) Size() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.size
}

// Workers returns a snapshot of running workers.
func (p *Pool) Workers() []WorkerStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]WorkerStatus, 0, len(p.workers))
	for _, w := range p.workers {
		out = append(out, WorkerStatus{
			ID:        w.id,
			Path:      w.path,
			Pipeline:  w.pipeline,
			StartedAt: w.startedAt,
		})
	}
	return out
}

// Resize updates the pool size. If shrinking below active count, schedules
// graceful termination after shrinkGrace.
func (p *Pool) Resize(size int, grace time.Duration, killOrder string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.size = size
	if grace > 0 {
		p.shrinkGrace = grace
	}
	if killOrder != "" {
		p.killOrder = killOrder
	}
	if len(p.workers) > size {
		if p.shrinkTimer != nil {
			p.shrinkTimer.Stop()
		}
		p.shrinkTimer = time.AfterFunc(p.shrinkGrace, p.killExcess)
	}
}

// killExcess terminates over-limit workers, sorted by startedAt.
func (p *Pool) killExcess() {
	p.mu.Lock()
	excess := len(p.workers) - p.size
	if excess <= 0 {
		p.mu.Unlock()
		return
	}
	ws := make([]*worker, 0, len(p.workers))
	for _, w := range p.workers {
		ws = append(ws, w)
	}
	sort.Slice(ws, func(i, j int) bool {
		if p.killOrder == "youngest" {
			return ws[i].startedAt.After(ws[j].startedAt)
		}
		return ws[i].startedAt.Before(ws[j].startedAt)
	})
	toKill := ws[:excess]
	p.mu.Unlock()

	for _, w := range toKill {
		log.Printf("[pool] shrink: stopping worker %s", w.id)
		w.cancel()
	}
}

// StopWorker sends cancellation to the worker for path.
func (p *Pool) StopWorker(taskID string) error {
	p.mu.Lock()
	w, ok := p.workers[taskID]
	p.mu.Unlock()
	if !ok {
		return fmt.Errorf("no active worker with id %q", taskID)
	}
	w.cancel()
	return nil
}

// Dispatch starts jobs for candidates up to the pool size limit.
func (p *Pool) Dispatch(candidates []*scanner.CandidateFile) {
	p.mu.Lock()
	slots := p.size - len(p.workers)
	p.mu.Unlock()
	if slots <= 0 {
		return
	}

	started := 0
	for _, c := range candidates {
		if started >= slots {
			break
		}
		id := taskID(c.Path)
		p.mu.Lock()
		_, running := p.workers[id]
		p.mu.Unlock()
		if running {
			continue
		}
		if err := p.startWorker(c); err != nil {
			log.Printf("[pool] dispatch %s: %v", c.Path, err)
		} else {
			started++
		}
	}
}

// startWorker launches a conversion job for c.
func (p *Pool) startWorker(c *scanner.CandidateFile) error {
	pipelineCfg, ok := p.pipelines[c.PipelineName]
	if !ok {
		return fmt.Errorf("unknown pipeline %q", c.PipelineName)
	}

	dbExtra, _ := p.store.GetPipelineExtra(c.PipelineName)
	extraJSON, err := executor.MergeExtra(pipelineCfg.Extra, dbExtra)
	if err != nil {
		return fmt.Errorf("merge extra: %w", err)
	}

	outputPath, err := executor.RenderTargetPath(c.Path, pipelineCfg.Target.Regex, pipelineCfg.Target.Format)
	if err != nil {
		return fmt.Errorf("render target: %w", err)
	}

	argv, err := executor.RenderCommand(pipelineCfg.Command, c.Path, outputPath, extraJSON)
	if err != nil {
		return fmt.Errorf("render command: %w", err)
	}

	if err := p.store.MarkInFlight(c.Path); err != nil {
		return fmt.Errorf("mark in_flight: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard

	if err := cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("start command: %w", err)
	}

	id := taskID(c.Path)
	w := &worker{
		id:        id,
		path:      c.Path,
		pipeline:  c.PipelineName,
		startedAt: time.Now(),
		cancel:    cancel,
		cmd:       cmd,
	}

	p.mu.Lock()
	p.workers[id] = w
	p.mu.Unlock()

	go p.wait(w, c.Path, c.PipelineName)
	log.Printf("[pool] started: %s → %s", c.Path, outputPath)
	return nil
}

// wait waits for a worker to finish and calls the onComplete callback.
func (p *Pool) wait(w *worker, path, pipeline string) {
	err := w.cmd.Wait()

	p.mu.Lock()
	delete(p.workers, w.id)
	p.mu.Unlock()

	w.cancel() // clean up context resources

	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == -1 {
			// Process was killed (context cancelled) — mark errored
			log.Printf("[pool] killed: %s", path)
		} else {
			log.Printf("[pool] error: %s: %v", path, err)
		}
	} else {
		log.Printf("[pool] completed: %s", path)
	}

	if p.onComplete != nil {
		p.onComplete(path, pipeline, err)
	}
}

// Shutdown cancels all running workers and waits for them to exit.
func (p *Pool) Shutdown(timeout time.Duration) {
	p.mu.Lock()
	workers := make([]*worker, 0, len(p.workers))
	for _, w := range p.workers {
		workers = append(workers, w)
	}
	p.mu.Unlock()

	for _, w := range workers {
		w.cancel()
	}

	deadline := time.Now().Add(timeout)
	for {
		p.mu.Lock()
		remaining := len(p.workers)
		p.mu.Unlock()
		if remaining == 0 {
			break
		}
		if time.Now().After(deadline) {
			log.Printf("[pool] shutdown timeout: %d workers still running", remaining)
			// Force kill
			p.mu.Lock()
			for _, w := range p.workers {
				if w.cmd.Process != nil {
					_ = w.cmd.Process.Kill()
				}
			}
			p.mu.Unlock()
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

}

// taskID encodes an absolute path as a URL-safe base64 string.
func taskID(path string) string {
	return base64.URLEncoding.EncodeToString([]byte(path))
}

// PathFromTaskID decodes a task ID back to a path.
func PathFromTaskID(id string) (string, error) {
	b, err := base64.URLEncoding.DecodeString(id)
	if err != nil {
		return "", fmt.Errorf("invalid task id: %w", err)
	}
	return string(b), nil
}
