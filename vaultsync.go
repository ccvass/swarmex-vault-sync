package vaultsync

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/client"
)

const (
	labelEnabled = "swarmex.vault.enabled"
	labelPath    = "swarmex.vault.path"
	labelRefresh = "swarmex.vault.refresh"
	labelSignal  = "swarmex.vault.signal"

	defaultRefresh   = 5 * time.Minute
	defaultSignal    = "SIGHUP"
	secretsDir       = "/run/secrets/swarmex"
)

// VaultConfig parsed from Docker service labels.
type VaultConfig struct {
	SecretPath string
	Refresh    time.Duration
	Signal     string
}

type syncState struct {
	config     VaultConfig
	lastHash   string
	cancelFunc context.CancelFunc
}

// Syncer watches Docker services and syncs secrets from OpenBao/Vault.
type Syncer struct {
	docker     *client.Client
	vaultAddr  string
	vaultToken string
	logger     *slog.Logger
	services   map[string]*syncState
	mu         sync.Mutex
	httpClient *http.Client
}

// New creates a Syncer.
func New(cli *client.Client, vaultAddr, vaultToken string, logger *slog.Logger) *Syncer {
	return &Syncer{
		docker:     cli,
		vaultAddr:  vaultAddr,
		vaultToken: vaultToken,
		logger:     logger,
		services:   make(map[string]*syncState),
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
}

// HandleEvent processes Docker service events.
func (s *Syncer) HandleEvent(ctx context.Context, event events.Message) {
	if event.Type != events.ServiceEventType {
		return
	}
	switch event.Action {
	case events.ActionCreate, events.ActionUpdate:
		s.reconcile(ctx, event.Actor.ID)
	case events.ActionRemove:
		s.stop(event.Actor.ID)
	}
}

func (s *Syncer) reconcile(ctx context.Context, serviceID string) {
	svc, _, err := s.docker.ServiceInspectWithRaw(ctx, serviceID, types.ServiceInspectOptions{})
	if err != nil {
		return
	}
	labels := svc.Spec.Labels
	if labels[labelEnabled] != "true" || labels[labelPath] == "" {
		s.stop(serviceID)
		return
	}

	cfg := parseVaultConfig(labels)

	s.mu.Lock()
	if existing, ok := s.services[serviceID]; ok {
		existing.cancelFunc()
	}
	syncCtx, cancel := context.WithCancel(ctx)
	state := &syncState{config: cfg, cancelFunc: cancel}
	s.services[serviceID] = state
	s.mu.Unlock()

	s.logger.Info("vault-sync watching service",
		"service", svc.Spec.Name, "path", cfg.SecretPath, "refresh", cfg.Refresh)

	go s.syncLoop(syncCtx, serviceID, svc.Spec.Name, state)
}

func (s *Syncer) syncLoop(ctx context.Context, serviceID, serviceName string, state *syncState) {
	// Sync immediately on start
	s.syncSecrets(ctx, serviceID, serviceName, state)

	ticker := time.NewTicker(state.config.Refresh)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.syncSecrets(ctx, serviceID, serviceName, state)
		case <-ctx.Done():
			return
		}
	}
}

func (s *Syncer) syncSecrets(ctx context.Context, serviceID, serviceName string, state *syncState) {
	secrets, err := s.readVaultSecret(ctx, state.config.SecretPath)
	if err != nil {
		s.logger.Error("vault read failed", "service", serviceName, "path", state.config.SecretPath, "error", err)
		return
	}

	// Serialize to compare with last known state
	data, _ := json.Marshal(secrets)
	hash := fmt.Sprintf("%x", data)

	if hash == state.lastHash {
		return // no change
	}
	state.lastHash = hash

	// Write secrets to filesystem
	dir := filepath.Join(secretsDir, serviceName)
	os.MkdirAll(dir, 0700)

	for key, value := range secrets {
		path := filepath.Join(dir, key)
		if err := os.WriteFile(path, []byte(value), 0600); err != nil {
			s.logger.Error("write secret failed", "path", path, "error", err)
			continue
		}
	}

	s.logger.Info("secrets synced", "service", serviceName, "keys", len(secrets))

	// Signal containers to reload
	s.signalContainers(ctx, serviceID, state.config.Signal)
}

func (s *Syncer) readVaultSecret(ctx context.Context, path string) (map[string]string, error) {
	url := fmt.Sprintf("%s/v1/%s", s.vaultAddr, path)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Vault-Token", s.vaultToken)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("vault returned %d", resp.StatusCode)
	}

	var result struct {
		Data struct {
			Data map[string]string `json:"data"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return result.Data.Data, nil
}

func (s *Syncer) signalContainers(ctx context.Context, serviceID, sig string) {
	// Find containers for this service and send signal
	containers, err := s.docker.ContainerList(ctx, container.ListOptions{})
	if err != nil {
		return
	}
	for _, c := range containers {
		if c.Labels["com.docker.swarm.service.id"] == serviceID {
			s.docker.ContainerKill(ctx, c.ID, sig)
		}
	}
}

func (s *Syncer) stop(serviceID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if state, ok := s.services[serviceID]; ok {
		state.cancelFunc()
		delete(s.services, serviceID)
	}
}

func parseVaultConfig(labels map[string]string) VaultConfig {
	cfg := VaultConfig{
		SecretPath: labels[labelPath],
		Refresh:    defaultRefresh,
		Signal:     defaultSignal,
	}
	if d, err := time.ParseDuration(labels[labelRefresh]); err == nil {
		cfg.Refresh = d
	}
	if v, ok := labels[labelSignal]; ok && v != "" {
		cfg.Signal = v
	}
	return cfg
}

// SignalFromName converts signal name to syscall.Signal (used for validation).
func SignalFromName(name string) syscall.Signal {
	signals := map[string]syscall.Signal{
		"SIGHUP":  syscall.SIGHUP,
		"SIGUSR1": syscall.SIGUSR1,
		"SIGUSR2": syscall.SIGUSR2,
		"SIGTERM": syscall.SIGTERM,
	}
	if s, ok := signals[name]; ok {
		return s
	}
	return syscall.SIGHUP
}

// keep strconv import used
var _ = strconv.Itoa
