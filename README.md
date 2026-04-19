# Swarmex Vault Sync

Syncs secrets from OpenBao (Vault) to Docker Swarm containers with hot-reload support.

Part of [Swarmex](https://github.com/ccvass/swarmex) — enterprise-grade orchestration for Docker Swarm.

## What It Does

Watches OpenBao KV v2 secret paths and syncs them into running containers. When secrets change, it can send a configurable signal to the container process for hot-reload without redeployment.

## Labels

```yaml
deploy:
  labels:
    swarmex.vault.enabled: "true"            # Enable secret syncing
    swarmex.vault.path: "secret/data/myapp"  # OpenBao KV v2 path
    swarmex.vault.refresh: "30"              # Seconds between sync checks
    swarmex.vault.signal: "SIGHUP"           # Signal to send on secret change
```

## How It Works

1. Discovers services with vault labels via Docker API.
2. Reads secrets from the configured OpenBao KV v2 path.
3. Injects secrets into the container's environment or mounted files.
4. Periodically checks for secret changes at the configured refresh interval.
5. Sends the configured signal to the container process when secrets are updated.

## Quick Start

```bash
docker service update \
  --label-add swarmex.vault.enabled=true \
  --label-add swarmex.vault.path=secret/data/myapp \
  --label-add swarmex.vault.signal=SIGHUP \
  my-app
```

## Verified

2 secrets synced successfully from OpenBao KV v2 into the target container.

## License

Apache-2.0
