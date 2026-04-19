package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"os"
	"strings"
	"os/signal"
	"syscall"

	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/client"

	vaultsync "github.com/ccvass/swarmex/swarmex-vault-sync"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	vaultAddr := os.Getenv("VAULT_ADDR")
	if vaultAddr == "" {
		vaultAddr = "http://openbao:8200"
	}
	vaultToken := os.Getenv("VAULT_TOKEN")
	if vaultToken == "" {
		if f := os.Getenv("VAULT_TOKEN_FILE"); f != "" {
			b, _ := os.ReadFile(f)
			vaultToken = strings.TrimSpace(string(b))
		}
	}

	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		logger.Error("failed to create Docker client", "error", err)
		os.Exit(1)
	}
	defer cli.Close()

	syncer := vaultsync.New(cli, vaultAddr, vaultToken, logger)

	go func() {
		http.Handle("/metrics", promhttp.Handler())
		http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, "ok")
		})
		logger.Info("health endpoint", "addr", ":8080")
		http.ListenAndServe(":8080", nil)
	}()

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	logger.Info("swarmex-vault-sync starting", "vault", vaultAddr)

	msgCh, errCh := cli.Events(ctx, events.ListOptions{})
	for {
		select {
		case event := <-msgCh:
			syncer.HandleEvent(ctx, event)
		case err := <-errCh:
			if ctx.Err() != nil {
				logger.Info("shutdown complete")
				return
			}
			logger.Error("event stream error", "error", err)
			return
		case <-ctx.Done():
			logger.Info("shutdown complete")
			return
		}
	}
}
