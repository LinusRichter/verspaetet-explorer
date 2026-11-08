// Command explorer downloads the published verspaetet dataset (on first
// start), prepares it for fast queries and serves the explorer UI.
//
// Configuration (environment):
//
//	MONTHS        which months to use: "latest" (default), "all" or "2026-09,2026-10"
//	DATA_DIR      where the data lives (default /data)
//	PORT          HTTP port inside the container (default 8080)
//	PUBLIC_URL    address printed for the user (default http://localhost:<PORT>)
//	THREADS       DuckDB worker threads (default 2)
//	MEMORY_LIMIT  DuckDB memory limit (default 800MB)
//	OFFLINE       "1": skip the dataset check, use what is already prepared
//	DATASET_REPO  HuggingFace dataset (default LinusRichter404/verspaetet)
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"strconv"
	"syscall"
	"time"

	"github.com/LinusRichter/verspaetet-explorer/internal/api"
	"github.com/LinusRichter/verspaetet-explorer/internal/dataset"
	"github.com/LinusRichter/verspaetet-explorer/internal/store"
)

func main() {
	log.SetFlags(log.Ltime)
	dataDir := env("DATA_DIR", "/data")
	port := env("PORT", "8080")
	publicURL := env("PUBLIC_URL", "http://localhost:"+port)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if env("OFFLINE", "") != "1" {
		src := dataset.Source{
			Endpoint: env("DATASET_ENDPOINT", "https://huggingface.co"),
			Repo:     env("DATASET_REPO", "LinusRichter404/verspaetet"),
			Client:   &http.Client{Timeout: 2 * time.Hour},
		}
		if err := dataset.Sync(ctx, src, dataDir, env("MONTHS", "latest")); err != nil {
			log.Fatalf("preparing the dataset failed: %v", err)
		}
		// Hand the memory used while splitting back before DuckDB starts.
		debug.FreeOSMemory()
	}

	threads, _ := strconv.Atoi(env("THREADS", "2"))
	st, err := store.Open(ctx, dataDir, store.Options{Threads: threads, MemoryLimit: env("MEMORY_LIMIT", "800MB")})
	if err != nil {
		log.Fatalf("opening the data failed: %v", err)
	}
	days := st.Days()
	if len(days) == 0 {
		log.Fatalf("no prepared data in %s (start once without OFFLINE=1)", dataDir)
	}

	mux := http.NewServeMux()
	api.Register(mux, st)
	mux.Handle("/", api.Static(env("WEB_DIR", "web/dist")))

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      200 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdown)
	}()

	log.Printf("data: %d days, %s to %s", len(days), days[0], days[len(days)-1])
	log.Printf("")
	log.Printf("  verspaetet-explorer is ready: open %s", publicURL)
	log.Printf("")
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
