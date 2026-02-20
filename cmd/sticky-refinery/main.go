package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/whisper-darkly/sticky-refinery/internal/api"
	"github.com/whisper-darkly/sticky-refinery/internal/config"
	"github.com/whisper-darkly/sticky-refinery/internal/daemon"
	"github.com/whisper-darkly/sticky-refinery/internal/db"
	"github.com/whisper-darkly/sticky-refinery/internal/hub"
	"github.com/whisper-darkly/sticky-refinery/internal/pool"
	"github.com/whisper-darkly/sticky-refinery/internal/store"
)

func main() {
	cfgPath := flag.String("config", "config.yaml", "path to config.yaml")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	if err := config.Validate(cfg); err != nil {
		log.Fatalf("invalid config: %v", err)
	}

	log.Printf("sticky-refinery starting: pool_size=%d scan_interval=%s pipelines=%d",
		cfg.Pool.Size, cfg.ScanInterval, len(cfg.Pipelines))

	database, err := db.Open(cfg.DBPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}

	st, err := store.New(database)
	if err != nil {
		log.Fatalf("init store: %v", err)
	}

	trustedNets := hub.DetectLocalSubnets()
	if cfg.TrustedCIDRs != "" {
		nets, err := hub.ParseTrustedCIDRs(cfg.TrustedCIDRs)
		if err != nil {
			log.Fatalf("parse trusted_cidrs: %v", err)
		}
		trustedNets = nets
	}
	h := hub.New(trustedNets)

	p := pool.New(cfg.Pool, st, cfg.Pipelines, daemon.OnComplete(st))
	d := daemon.New(cfg, st, p)

	srv := api.New(cfg, *cfgPath, st, p, h)
	httpServer := &http.Server{
		Addr:    cfg.ListenAddr,
		Handler: srv.Router(),
	}

	d.Start()
	log.Printf("listening on %s", cfg.ListenAddr)

	go func() {
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("http server: %v", err)
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	log.Println("shutdown: received signal")

	d.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := httpServer.Shutdown(ctx); err != nil {
		log.Printf("http shutdown: %v", err)
	}

	p.Shutdown(5 * time.Minute)
	log.Println("shutdown complete")
}
