package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/whisper-darkly/sticky-refinery/internal/config"
	"github.com/whisper-darkly/sticky-refinery/internal/overseer"
	"github.com/whisper-darkly/sticky-refinery/internal/pool"
	"github.com/whisper-darkly/sticky-refinery/internal/store"
)

// Server holds the API dependencies.
type Server struct {
	cfg        *config.Config
	cfgPath    string
	store      *store.Store
	pool       *pool.Pool
	hub        *overseer.Hub
	wsHandler  http.HandlerFunc
}

// New creates a Server.
func New(cfg *config.Config, cfgPath string, st *store.Store, p *pool.Pool, hub *overseer.Hub, wsHandler http.HandlerFunc) *Server {
	return &Server{
		cfg:       cfg,
		cfgPath:   cfgPath,
		store:     st,
		pool:      p,
		hub:       hub,
		wsHandler: wsHandler,
	}
}

// Router returns the chi router with all routes registered.
func (s *Server) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	r.Get("/health", s.handleHealth)
	r.Get("/config", s.handleGetConfig)
	r.Get("/pool", s.handleGetPool)
	r.Patch("/pool", s.handlePatchPool)
	r.Get("/pipelines", s.handleListPipelines)
	r.Get("/pipelines/{name}", s.handleGetPipeline)
	r.Patch("/pipelines/{name}", s.handlePatchPipeline)
	r.Get("/tasks", s.handleListTasks)
	r.Get("/tasks/{id}", s.handleGetTask)
	r.Post("/tasks/{id}/stop", s.handleStopTask)
	r.Post("/tasks/{id}/pause", s.handlePauseTask)
	r.Post("/tasks/{id}/resume", s.handleResumeTask)

	if s.wsHandler != nil {
		r.Get("/ws", s.wsHandler)
	}

	return r
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, cfg)
}

func (s *Server) handleGetPool(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"size":        s.pool.Size(),
		"active":      s.pool.ActiveCount(),
		"workers":     s.pool.Workers(),
	})
}

type patchPoolRequest struct {
	Size           *int    `json:"size"`
	ShrinkGrace    *string `json:"shrink_grace"`
	ShrinkKillOrder *string `json:"shrink_kill_order"`
}

func (s *Server) handlePatchPool(w http.ResponseWriter, r *http.Request) {
	var req patchPoolRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	size := s.pool.Size()
	if req.Size != nil {
		size = *req.Size
	}

	var grace config.Duration
	if req.ShrinkGrace != nil {
		if err := grace.UnmarshalJSON([]byte(*req.ShrinkGrace)); err != nil {
			writeError(w, http.StatusBadRequest, "invalid shrink_grace: "+err.Error())
			return
		}
	}

	killOrder := ""
	if req.ShrinkKillOrder != nil {
		killOrder = *req.ShrinkKillOrder
	}

	s.pool.Resize(size, grace.Duration, killOrder)

	// Persist to pipeline_config table under "__pool__"
	b, _ := json.Marshal(req)
	_ = s.store.SetPipelineExtra("__pool__", string(b))

	writeJSON(w, http.StatusOK, map[string]any{
		"size":   s.pool.Size(),
		"active": s.pool.ActiveCount(),
	})
}

func (s *Server) handleListPipelines(w http.ResponseWriter, r *http.Request) {
	type pipelineItem struct {
		Name     string `json:"name"`
		Priority int    `json:"priority"`
		Stats    any    `json:"stats"`
	}
	var items []pipelineItem
	for _, p := range s.cfg.Pipelines {
		stats, _ := s.store.GetPipelineStats(p.Name)
		items = append(items, pipelineItem{
			Name:     p.Name,
			Priority: p.Priority,
			Stats:    stats,
		})
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) handleGetPipeline(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	var pCfg *config.PipelineConfig
	for i := range s.cfg.Pipelines {
		if s.cfg.Pipelines[i].Name == name {
			pCfg = &s.cfg.Pipelines[i]
			break
		}
	}
	if pCfg == nil {
		writeError(w, http.StatusNotFound, "pipeline not found")
		return
	}
	extra, _ := s.store.GetPipelineExtra(name)
	stats, _ := s.store.GetPipelineStats(name)
	writeJSON(w, http.StatusOK, map[string]any{
		"config": pCfg,
		"extra":  json.RawMessage(extra),
		"stats":  stats,
	})
}

func (s *Server) handlePatchPipeline(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	found := false
	for _, p := range s.cfg.Pipelines {
		if p.Name == name {
			found = true
			break
		}
	}
	if !found {
		writeError(w, http.StatusNotFound, "pipeline not found")
		return
	}

	var extra map[string]any
	if err := json.NewDecoder(r.Body).Decode(&extra); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	b, _ := json.Marshal(extra)
	if err := s.store.SetPipelineExtra(name, string(b)); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"extra": extra})
}

func (s *Server) handleListTasks(w http.ResponseWriter, r *http.Request) {
	pipeline := r.URL.Query().Get("pipeline")
	status := r.URL.Query().Get("status")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))

	tasks, err := s.store.ListTasks(pipeline, status, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, tasks)
}

func (s *Server) handleGetTask(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	path, err := pool.PathFromTaskID(id)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	tf, err := s.store.GetByPath(path)
	if err != nil {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}
	writeJSON(w, http.StatusOK, tf)
}

func (s *Server) handleStopTask(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.pool.StopWorker(id); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "stopping"})
}

func (s *Server) handlePauseTask(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	path, err := pool.PathFromTaskID(id)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Stop if running (best effort)
	_ = s.pool.StopWorker(id)
	if err := s.store.MarkPaused(path); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "paused"})
}

func (s *Server) handleResumeTask(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	path, err := pool.PathFromTaskID(id)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.store.MarkResumed(path); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "queued"})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
