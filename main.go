package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/VikramjeetRaj/ulysses-obsidian-mcp/banner"
	"github.com/VikramjeetRaj/ulysses-obsidian-mcp/lib/config"
	"github.com/VikramjeetRaj/ulysses-obsidian-mcp/lib/index"
	"github.com/VikramjeetRaj/ulysses-obsidian-mcp/lib/logger"
	"github.com/VikramjeetRaj/ulysses-obsidian-mcp/lib/vault"
	"github.com/VikramjeetRaj/ulysses-obsidian-mcp/tools/indexer"
	mcpserver "github.com/VikramjeetRaj/ulysses-obsidian-mcp/tools/mcp"
)

// reconcileInterval is how often a full sync recovers anything the watcher missed.
const reconcileInterval = 10 * time.Minute

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	// Standard output carries MCP protocol messages only, so the banner goes to stderr.
	fmt.Fprintln(os.Stderr, banner.BANNER)
	cfg, err := config.Load("config.yaml")
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	log, err := logger.New(cfg.LogPath)
	if err != nil {
		return fmt.Errorf("start logger: %w", err)
	}
	vaultSvc, err := vault.New(cfg.Vault, log)
	if err != nil {
		log.Error("start vault", "err", err)
		return fmt.Errorf("start vault: %w", err)
	}
	indexSvc, err := index.New(cfg.IndexPath, log)
	if err != nil {
		log.Error("start index", "err", err)
		return fmt.Errorf("start index: %w", err)
	}
	defer indexSvc.Close()

	knowledge := filepath.Join(cfg.Vault, "Knowledge")
	indexerSvc, err := indexer.New(knowledge, indexSvc, log)
	if err != nil {
		return fmt.Errorf("start indexer: %w", err)
	}
	server := mcpserver.New(vaultSvc, indexSvc, indexerSvc, knowledge, log)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

	// finished receives the result of each long-running task; any result ends the program.
	finished := make(chan error, 3)
	var wg sync.WaitGroup
	defer wg.Wait()
	wg.Add(3)
	// Start watching before the first sync so changes made during it are not missed.
	go func() {
		defer wg.Done()
		if err := indexerSvc.Watch(ctx); err != nil {
			finished <- fmt.Errorf("watch: %w", err)
		}
	}()
	go func() {
		defer wg.Done()
		if err := indexerSvc.Reconcile(ctx, reconcileInterval); err != nil {
			finished <- fmt.Errorf("reconcile: %w", err)
		}
	}()
	// The server returns when the client disconnects.
	go func() {
		defer wg.Done()
		if err := server.Run(ctx); err != nil && ctx.Err() == nil {
			finished <- fmt.Errorf("serve: %w", err)
			return
		}
		finished <- nil
	}()
	defer stop() // deferred last so it runs before wg.Wait and the goroutines see the cancellation

	// Searches work from the first request but may miss notes until this first sync is done.
	// A file that fails to index is not fatal; the periodic reconcile retries it.
	go func() {
		if _, err := indexerSvc.Sync(ctx); err != nil && ctx.Err() == nil {
			log.Warn("initial index sync incomplete", "err", err)
		}
	}()

	select {
	case <-ctx.Done():
		return nil
	case err := <-finished:
		return err
	}

}
