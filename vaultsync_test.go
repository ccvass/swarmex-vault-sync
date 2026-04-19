package vaultsync

import (
	"testing"
	"time"
)

func TestParseVaultConfig(t *testing.T) {
	tests := []struct {
		name   string
		labels map[string]string
		want   VaultConfig
	}{
		{
			"defaults",
			map[string]string{labelPath: "secret/data/myapp"},
			VaultConfig{SecretPath: "secret/data/myapp", Refresh: 5 * time.Minute, Signal: "SIGHUP"},
		},
		{
			"custom",
			map[string]string{
				labelPath:    "secret/data/api",
				labelRefresh: "30s",
				labelSignal:  "SIGUSR1",
			},
			VaultConfig{SecretPath: "secret/data/api", Refresh: 30 * time.Second, Signal: "SIGUSR1"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseVaultConfig(tt.labels)
			if got != tt.want {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}
