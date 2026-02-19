package daemon

import (
	"errors"
	"log"
	"os"
	"syscall"
	"time"

	"github.com/whisper-darkly/sticky-refinery/internal/config"
	"github.com/whisper-darkly/sticky-refinery/internal/pool"
	"github.com/whisper-darkly/sticky-refinery/internal/scanner"
	"github.com/whisper-darkly/sticky-refinery/internal/store"
)

// Daemon runs the scan-dispatch loop.
type Daemon struct {
	cfg      *config.Config
	store    *store.Store
	pool     *pool.Pool
	ticker   *time.Ticker
	stopCh   chan struct{}
	doneCh   chan struct{}
}

// New creates a Daemon. It does not start the loop.
func New(cfg *config.Config, st *store.Store, p *pool.Pool) *Daemon {
	return &Daemon{
		cfg:    cfg,
		store:  st,
		pool:   p,
		stopCh: make(chan struct{}),
		doneCh: make(chan struct{}),
	}
}

// Start begins the scan-dispatch loop in a goroutine.
func (d *Daemon) Start() {
	d.ticker = time.NewTicker(d.cfg.ScanInterval.Duration)
	go d.run()
}

// Stop signals the daemon to stop and waits for it to exit.
func (d *Daemon) Stop() {
	close(d.stopCh)
	<-d.doneCh
}

func (d *Daemon) run() {
	defer close(d.doneCh)
	// Run an initial scan immediately.
	d.scanAndDispatch()
	for {
		select {
		case <-d.ticker.C:
			d.scanAndDispatch()
		case <-d.stopCh:
			d.ticker.Stop()
			return
		}
	}
}

func (d *Daemon) scanAndDispatch() {
	candidates, err := scanner.ScanAll(d.cfg.Pipelines)
	if err != nil {
		log.Printf("[daemon] scan error: %v", err)
		return
	}

	// Filter out paths already tracked (queued/in_flight/paused).
	var fresh []*scanner.CandidateFile
	for _, c := range candidates {
		tf, err := d.store.GetByPath(c.Path)
		if err != nil {
			// Not in DB — enqueue it.
			if err2 := d.store.UpsertQueued(c.Path, c.PipelineName); err2 != nil {
				log.Printf("[daemon] upsert %s: %v", c.Path, err2)
				continue
			}
			fresh = append(fresh, c)
			continue
		}
		switch tf.Status {
		case "queued", "errored":
			fresh = append(fresh, c)
		case "paused", "in_flight", "completed":
			// skip
		}
	}

	if len(fresh) > 0 {
		log.Printf("[daemon] dispatching %d candidates", len(fresh))
		d.pool.Dispatch(fresh)
	}
}

// OnComplete is the callback wired into the pool.
// It deletes the input file on success or records the error.
func OnComplete(st *store.Store) pool.OnCompleteFunc {
	return func(path, pipeline string, err error) {
		if err != nil {
			if err2 := st.MarkErrored(path, err.Error()); err2 != nil {
				log.Printf("[daemon] mark errored %s: %v", path, err2)
			}
			return
		}
		if err2 := st.MarkCompleted(path); err2 != nil {
			log.Printf("[daemon] mark completed %s: %v", path, err2)
		}
		if err2 := removeFileWithRetry(path, 4, 250*time.Millisecond); err2 != nil {
			log.Printf("[daemon] delete input %s: %v", path, err2)
		}
	}
}

// removeFileWithRetry attempts to remove path with retries for transient errors.
// Ported from chaturbate-dvr/server/converter.go.
func removeFileWithRetry(path string, attempts int, baseDelay time.Duration) error {
	if attempts <= 0 {
		attempts = 1
	}
	if baseDelay <= 0 {
		baseDelay = 100 * time.Millisecond
	}
	_ = os.Chmod(path, 0666)
	var lastErr error
	for i := 0; i < attempts; i++ {
		err := os.Remove(path)
		if err == nil || errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) ||
			errors.Is(err, syscall.EBUSY) || errors.Is(err, syscall.ETXTBSY) {
			_ = os.Chmod(path, 0666)
			time.Sleep(baseDelay * time.Duration(i+1))
			lastErr = err
			continue
		}
		return err
	}
	return lastErr
}
