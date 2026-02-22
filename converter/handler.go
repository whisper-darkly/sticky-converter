package converter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"syscall"
	"time"

	overseer "github.com/whisper-darkly/sticky-overseer/v2"
	"github.com/whisper-darkly/sticky-refinery/internal/db"
	"github.com/whisper-darkly/sticky-refinery/internal/executor"
	"github.com/whisper-darkly/sticky-refinery/internal/scanner"
	"github.com/whisper-darkly/sticky-refinery/internal/store"
)

// duration is a time.Duration that JSON-unmarshals from strings like "30s".
type duration struct {
	time.Duration
}

func (d *duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	if s == "" || s == "null" {
		return nil
	}
	dur, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	d.Duration = dur
	return nil
}

type targetConfig struct {
	Regex  string `json:"regex,omitempty"`
	Format string `json:"format"`
}

type converterConfig struct {
	ScanInterval    duration     `json:"scan_interval"`
	Paths           []string     `json:"paths"`
	Direction       string       `json:"direction"`
	MinAge          duration     `json:"min_age,omitempty"`
	MaxAge          duration     `json:"max_age,omitempty"`
	Target          targetConfig `json:"target"`
	Command         string       `json:"command"`
	DBPath          string       `json:"db_path,omitempty"`
	DeleteOnSuccess bool         `json:"delete_on_success"`
}

type converterHandler struct {
	actionName string
	cfg        converterConfig
	store      *store.Store
}

// Describe returns metadata about this handler for introspection.
func (h *converterHandler) Describe() overseer.ActionInfo {
	fileParam := &overseer.ParamSpec{} // Default=nil means required
	return overseer.ActionInfo{
		Name:   h.actionName,
		Type:   "converter",
		Params: map[string]*overseer.ParamSpec{"file": fileParam},
	}
}

// Validate checks that params satisfy the handler's requirements.
func (h *converterHandler) Validate(params map[string]string) error {
	if params["file"] == "" {
		return fmt.Errorf("converter: required parameter \"file\" is missing")
	}
	return nil
}

// Start launches an ffmpeg worker for the given file.
func (h *converterHandler) Start(taskID string, params map[string]string, cb overseer.WorkerCallbacks) (*overseer.Worker, error) {
	inputPath := params["file"]
	if inputPath == "" {
		return nil, fmt.Errorf("converter: missing required param \"file\"")
	}

	outputPath, err := executor.RenderTargetPath(inputPath, h.cfg.Target.Regex, h.cfg.Target.Format)
	if err != nil {
		return nil, fmt.Errorf("converter: render target path: %w", err)
	}

	argv, err := executor.RenderCommand(h.cfg.Command, inputPath, outputPath, "{}")
	if err != nil {
		return nil, fmt.Errorf("converter: render command: %w", err)
	}
	if len(argv) == 0 {
		return nil, fmt.Errorf("converter: command rendered to empty argv")
	}

	if err := h.store.MarkInFlight(inputPath); err != nil {
		log.Printf("[converter] mark in_flight %s: %v", inputPath, err)
	}

	deleteOnSuccess := h.cfg.DeleteOnSuccess
	st := h.store

	wrappedCB := overseer.NewWorkerCallbacks(
		cb.OnOutput,
		cb.LogEvent,
		func(w *overseer.Worker, exitCode int, intentional bool, t time.Time) {
			if exitCode == 0 {
				if err := st.MarkCompleted(inputPath); err != nil {
					log.Printf("[converter] mark completed %s: %v", inputPath, err)
				}
				if deleteOnSuccess {
					if err := removeFileWithRetry(inputPath, 4, 250*time.Millisecond); err != nil {
						log.Printf("[converter] delete input %s: %v", inputPath, err)
					}
				}
			} else {
				errMsg := fmt.Sprintf("exit code %d", exitCode)
				if err := st.MarkErrored(inputPath, errMsg); err != nil {
					log.Printf("[converter] mark errored %s: %v", inputPath, err)
				}
			}
			cb.OnExited(w, exitCode, intentional, t)
		},
	)

	workerCfg := overseer.WorkerConfig{
		TaskID:        taskID,
		Command:       argv[0],
		Args:          argv[1:],
		IncludeStdout: true,
		IncludeStderr: true,
	}
	return overseer.StartWorker(workerCfg, wrappedCB)
}

// RunService implements overseer.ServiceHandler — the directory scan loop.
// The hub calls RunService once at startup; it blocks until ctx is cancelled.
func (h *converterHandler) RunService(ctx context.Context, submit overseer.TaskSubmitter) {
	scanInterval := h.cfg.ScanInterval.Duration
	if scanInterval <= 0 {
		scanInterval = 30 * time.Second
	}

	// Initial scan immediately.
	h.scan(submit)

	ticker := time.NewTicker(scanInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			h.scan(submit)
		}
	}
}

func (h *converterHandler) scan(submit overseer.TaskSubmitter) {
	paths, err := scanner.ScanAll(h.cfg.Paths, h.cfg.Direction, h.cfg.MinAge.Duration, h.cfg.MaxAge.Duration)
	if err != nil {
		log.Printf("[converter] scan error: %v", err)
		return
	}

	for _, path := range paths {
		if h.store.IsCompleted(path) || h.store.IsInFlight(path) {
			continue
		}
		if err := h.store.UpsertQueued(path, h.actionName); err != nil {
			log.Printf("[converter] upsert queued %s: %v", path, err)
			continue
		}
		if err := submit.Submit(h.actionName, "", map[string]string{"file": path}); err != nil {
			log.Printf("[converter] submit %s: %v", path, err)
		}
	}
}

// ---------------------------------------------------------------------------
// converterFactory — registers the "converter" driver at init() time
// ---------------------------------------------------------------------------

type converterFactory struct{}

func (f *converterFactory) Type() string { return "converter" }

// Create instantiates a converterHandler from the raw config map.
func (f *converterFactory) Create(config map[string]any, actionName string, mergedRetry overseer.RetryPolicy, poolCfg overseer.PoolConfig, dedupeKey []string) (overseer.ActionHandler, error) {
	raw, err := json.Marshal(config)
	if err != nil {
		return nil, fmt.Errorf("converter: failed to marshal config: %w", err)
	}

	var cfg converterConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("converter: failed to parse config: %w", err)
	}

	if len(cfg.Paths) == 0 {
		return nil, fmt.Errorf("converter: config.paths is required")
	}
	if cfg.Target.Format == "" {
		return nil, fmt.Errorf("converter: config.target.format is required")
	}
	if cfg.Command == "" {
		return nil, fmt.Errorf("converter: config.command is required")
	}
	if cfg.Direction == "" {
		cfg.Direction = "oldest"
	}

	dbPath := cfg.DBPath
	if dbPath == "" {
		dbPath = "sticky-refinery.db"
	}

	database, err := db.Open(dbPath)
	if err != nil {
		return nil, fmt.Errorf("converter: open db %s: %w", dbPath, err)
	}

	st, err := store.New(database)
	if err != nil {
		database.Close()
		return nil, fmt.Errorf("converter: init store: %w", err)
	}

	return &converterHandler{
		actionName: actionName,
		cfg:        cfg,
		store:      st,
	}, nil
}

func init() {
	overseer.RegisterFactory(&converterFactory{})
}

// removeFileWithRetry attempts to remove path with retries for transient errors.
func removeFileWithRetry(path string, attempts int, baseDelay time.Duration) error {
	if attempts <= 0 {
		attempts = 1
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
