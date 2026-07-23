[![MIT License](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

# incuse

Ephemeral GitHub Actions runners on [Incus](https://linuxcontainers.org/incus/)
VMs.

## What

A single-host orchestrator that owns one GitHub Actions Runner Scale Set per
configured CPU/memory/disk/architecture class. It maintains GitHub's desired
number of ephemeral JIT runners by launching fresh Incus VMs. Each VM powers
itself off when the runner exits; a reaper cleans up stragglers.

```mermaid
graph LR
    GH[GitHub Actions scale sets] -->|long-poll| O[incuse]
    O -->|mint JIT| GH
    O -->|launch VM with cloud-init| I[Incus]
    I --> V1[VM] & V2[VM]
    V1 -->|run runner.sh, then poweroff| GH
```

Designed to run as a `systemd` service on the same host as the Incus daemon,
talking to it over the local Unix socket. HTTPS+cert is also supported for
off-host deployments.

## Status

Early. See the project plan for the active phase.

## Install

On a host running Incus, with the `incuse` system user pre-created (or letting `install.sh` create it):

```bash
bash deploy/systemd/install.sh ./bin/incuse
# edit /etc/incuse/config.yaml, drop a chmod-600 PAT at /etc/incuse/github.pat
systemctl enable --now incuse
```

Full walkthrough: [`docs/deployment.md`](docs/deployment.md). Day-2 ops: [`docs/operations.md`](docs/operations.md).

## Build

```bash
make            # build ./bin/incuse
make test       # go test -race ./...
make lint       # golangci-lint run
```

## License

MIT — see [LICENSE](LICENSE).
