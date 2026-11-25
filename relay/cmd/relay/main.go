package main

import (
	"context"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"phone-ai-audit/relay/internal/api"
	"phone-ai-audit/relay/internal/config"
	"phone-ai-audit/relay/internal/hub"
	"phone-ai-audit/relay/internal/store"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	st, err := store.Open(cfg.DBPath)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer st.Close()

	h := hub.New(st)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	h.StartBackground(ctx)

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           api.New(cfg, st, h),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("ai-audit-relay listening on %s (db=%s)", cfg.ListenAddr, cfg.DBPath)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	<-ctx.Done()
	log.Printf("shutting down ai-audit-relay...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}
