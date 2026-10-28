[简体中文](README.md) | **English**

# makebox (`mkbox`)

[![Go](https://img.shields.io/badge/Go-1.22+-00ADD8?style=flat-square&logo=go&logoColor=white)](https://go.dev)
[![License](https://img.shields.io/badge/License-MIT-green?style=flat-square)](LICENSE)
[![Release](https://img.shields.io/badge/Release-v0.2.0-blue?style=flat-square)](https://github.com/makebox/makebox/releases)
[![Platform](https://img.shields.io/badge/Platform-Linux%20amd64%20%7C%20arm64%20%7C%20arm-lightgrey?style=flat-square)](#supported-distributions)

> A previewable, recoverable Docker and Kubernetes operations CLI: plan, apply, roll back, verify — one set of commands.

`makebox` (command name **`mkbox`**) is a single static binary. It does not require Python, Ansible, or a long-running agent. It turns Docker installation, Kubernetes clusters, registry mirrors, system proxies, package mirror switching, configuration snapshots, and offline distribution into commands you can plan, apply, roll back, and verify.

It is not just another install script. It focuses on what happens after installation: whether configuration has drifted, whether a failed change can be recovered, whether Docker and Kubernetes runtimes conflict, and whether offline artifacts can be trusted. Installation is the beginning, not the whole job.

## Features

- **Cross-distribution Docker installation**: detects apt / dnf / yum / zypper / pacman / apk, writes the matching package repository, and installs Docker Engine, Buildx, and the Compose plugin.
- **One-command Kubernetes**: supports kubeadm and k3s, including preflight, containerd configuration, component installation, `kubeadm init`, CNI installation, and kubeconfig export.
- **Node join and reset**: generates join commands, executes control-plane scripts, supports a control-plane endpoint, and resets a node safely.
- **Registry mirrors and proxies**: ships with Docker Hub mirror candidates, probes `GET /v2/`, sorts by latency, and writes Docker, containerd, and k3s configuration. Proxy scopes cover `/etc/environment`, `profile.d`, apt, dockerd, and containerd.
- **OS and developer mirrors**: `mirror os` covers Debian, RHEL, SUSE, Arch, and Alpine (guidance only where repositories differ significantly); `mirror dev` configures pip, npm, and Go module mirrors in one step.
- **Transactional convergence**: `plan` / `diff` / `apply` / `rollback` compare desired state with the host, execute actions, snapshot managed files in a journal, and support automatic rollback on failure.
- **Runtime coexistence diagnostics**: inspects Docker's bundled containerd, system containerd, cri-dockerd, k3s, kubelet sockets, cgroup drivers, iptables backends, swap, and port conflicts.
- **Verifiable offline distribution**: `bundle` produces deterministic tar.gz archives with SPDX SBOMs and Ed25519 signatures, and can push/pull them as OCI artifacts with digest and signature verification.
- **Security first**: backs up files before writing, uses atomic writes, preserves unknown `daemon.json` fields, redacts proxy credentials, and protects journals, reports, and private keys with `0600` permissions.
- **Chinese and English UI**: ships `zh-CN` / `en` message catalogs selected by `--lang`, `MKBOX_LANG`, or the `language` config key; untranslated dynamic text falls back to Chinese.

## Installation

### Release binaries

Download the archive for your architecture from [Releases](https://github.com/makebox/makebox/releases):

```bash
tar -xzf mkbox-linux-amd64.tar.gz
sudo install -m 0755 mkbox /usr/local/bin/mkbox
mkbox version
```

Release artifacts:

| File | Description |
| --- | --- |
| `mkbox-linux-amd64.tar.gz` | Linux x86_64 |
| `mkbox-linux-arm64.tar.gz` | Linux ARM64 |
| `mkbox-linux-arm.tar.gz` | Linux ARMv7 |
| `makebox-<version>-source.tar.gz` | Complete source archive |
| `checksums.txt` | SHA-256 checksums for all artifacts |

### Install script

```bash
sudo ./install.sh
```

The script detects the CPU architecture, downloads from `MKBOX_BASE_URL` (GitHub Releases by default), and always verifies the SHA-256 checksums from `checksums.txt`; installation aborts when the checksum file cannot be fetched. For offline installations:

```bash
sudo ./install.sh --local ./dist/mkbox-linux-amd64.tar.gz
```

### Build from source

Go 1.22 or newer is required:

```bash
git clone https://github.com/makebox/makebox.git
cd makebox
make build          # writes bin/mkbox
sudo make install   # installs to /usr/local/bin
```

The result is a single static binary. Target machines do not need Go, Python, or any other runtime.

## Quick start

Prepare a host in four steps. Every step can run with `--dry-run` first; all system changes follow explicit configuration, not a hidden lock file.

```bash
# 1. Check the host
sudo mkbox doctor

# 2. Install Docker with registry mirrors and log rotation
sudo mkbox docker install

# 3. Install and initialize a single-node Kubernetes cluster
sudo mkbox k8s up

# 4. Compare desired state with the host
mkbox plan all
```

Common variations:

```bash
# Use the official Docker package repository
sudo mkbox docker install --mirror official

# Apply only registry mirrors that respond to probes
sudo mkbox docker install --probe

# Use k3s
sudo mkbox k8s up --distro k3s

# Use Calico with a custom pod CIDR
sudo mkbox k8s up --cni calico --pod-cidr 10.244.0.0/16

# Print the worker join command
sudo mkbox k8s join-command
```

## Convergence and rollback

Turn every change into a transaction you can undo. Desired state comes from the configuration file, not a hidden lock file; after changing mirror settings, `data-root`, Kubernetes options, or proxy settings, `plan` / `diff` immediately show the difference.

```bash
# Read-only actions, risks, and file diffs
sudo mkbox plan all

# File diffs only; exits with 2 when drift exists, 1 on command failure
mkbox diff all --exit-code

# Apply a plan; irreversible actions require --force
sudo mkbox apply docker --force

# Roll back completed file changes if any step fails
sudo mkbox apply docker --auto-rollback --force

# Roll back the latest transaction, or a specific journal
sudo mkbox rollback --dry-run
sudo mkbox rollback
sudo mkbox rollback <journal-id>
```

Before each action, `apply` snapshots managed files into `/var/lib/mkbox/journals`. Journals may only reference paths under `/etc` and are written with `0600` permissions. Rollback restores files and restarts services that were active before the transaction, but it **does not** uninstall packages or reset clusters.

Generate an archival evidence report:

```bash
mkbox report -o /var/tmp/mkbox-report.json
```

The report contains the host fingerprint, full plan, runtime topology, and journal list, and is written with `0600` permissions.

## Runtime coexistence diagnostics

Docker, containerd, cri-dockerd, k3s, and kubelet can coexist on the same machine. The `runtime` commands only inspect and advise; they never modify CRI sockets, cgroup drivers, or iptables backends.

```bash
mkbox runtime detect
mkbox runtime plan
mkbox runtime plan --strict   # exits non-zero on blocking findings
```

The checks include:

- Socket ownership between Docker's bundled containerd and the system containerd
- Whether kubelet still points at the removed dockershim endpoint
- Whether Docker and kubelet use different cgroup drivers
- cgroup v1 / v2 and iptables / nft backends
- Swap, CRI sockets, and ports such as 6443 / 10250 / 2375

## Offline distribution and OCI

Offline artifacts can be verified, not just trusted. `bundle` does not depend on external packaging tools; each archive contains a manifest, file digests, an SPDX 2.3 SBOM, checksums, and an optional signature.

```bash
# Generate Ed25519 keys; keep the private key offline
mkbox bundle keygen --out-dir ./keys

# Build a deterministic, signed bundle with an SBOM
mkbox bundle build \
  --name mkbox-offline --version 0.2.0 \
  --platform linux/amd64 --license MIT \
  --created-at 2026-01-01T00:00:00Z \
  --key ./keys/mkbox-bundle.key \
  --file ./dist/mkbox-linux-amd64.tar.gz \
  -o ./mkbox-offline.mkbox.tar.gz

# Verify digests, SBOM, checksums, and signature
mkbox bundle verify ./mkbox-offline.mkbox.tar.gz \
  --key ./keys/mkbox-bundle.pub --require-signature

# Push and pull as an OCI artifact
mkbox bundle push ./mkbox-offline.mkbox.tar.gz \
  --ref registry.example.com/tools/mkbox-offline:0.2.0
mkbox bundle pull \
  --ref registry.example.com/tools/mkbox-offline:0.2.0 \
  -o ./mkbox-offline.mkbox.tar.gz
```

The same inputs, fixed timestamp, and signing key produce a byte-identical bundle. Verification rejects path traversal, duplicate entries, digest mismatches, missing SBOMs, invalid checksums, unsigned artifacts, and untrusted signing keys. Unsigned artifacts require an explicit `--allow-unsigned` (recommended only for local build artifacts).

## Supported distributions

Covers mainstream Linux distributions and adapts to the package manager automatically; when repositories differ significantly, it says so instead of guessing.

| Family | Examples | Docker | kubeadm | k3s | OS package mirror switching |
| --- | --- | --- | --- | --- | --- |
| Debian | Ubuntu, Debian, Linux Mint, Raspbian | apt | Supported | Supported (systemd) | Supported |
| RHEL | RHEL, Rocky, AlmaLinux, Fedora, CentOS Stream | dnf/yum | Supported | Supported (systemd) | Guidance only |
| SUSE | openSUSE Leap/Tumbleweed, SLES | zypper | Not recommended | Supported (systemd) | Guidance only |
| Arch | Arch, Manjaro, EndeavourOS | pacman | Not recommended | Supported (systemd) | Supported |
| Alpine | Alpine Linux | apk | Not supported | Not supported (no systemd) | Supported |

Notes:

- Automated kubeadm installation currently covers Debian/Ubuntu and RHEL families. Other distributions should use `--distro k3s`.
- k3s requires systemd. Alpine uses openrc, so mkbox only provides Docker, proxy, mirror, and package mirror support there.
- RHEL/SUSE repository layouts vary significantly. `mkbox mirror os` does not rewrite them and only prints guidance.

## Command overview

| Area | Commands | Description |
| --- | --- | --- |
| Host | `doctor`, `net test` | Host checks and network connectivity |
| Docker | `docker install`, `docker status`, `docker uninstall` | Install, inspect, and remove Docker |
| Docker config | `docker mirror set/unset/list/show/test`, `docker proxy set/unset/show`, `docker data-root` | Registry mirrors, daemon proxy, data root |
| Kubernetes | `k8s preflight`, `k8s install`, `k8s init`, `k8s up` | Preflight, install, and initialize a single-node cluster |
| Kubernetes ops | `k8s join`, `k8s join-command`, `k8s addon`, `k8s kubeconfig`, `k8s status`, `k8s reset` | Join nodes, addons, kubeconfig, status, reset |
| Mirrors and proxies | `registry`, `proxy`, `mirror` | Registry probing, system proxy, package and developer mirrors |
| Convergence | `plan`, `diff`, `apply`, `rollback`, `report` | Plan, diff, transactional apply, rollback, evidence report |
| Runtime | `runtime detect`, `runtime plan` | Docker / containerd / Kubernetes coexistence diagnostics |
| Snapshots and distribution | `backup`, `bundle`, `config` | Configuration snapshots, signed offline bundles, configuration management |

See [docs/commands.md](docs/commands.md) for full flags and examples.

## Configuration and state files

| Item | Default location | Override |
| --- | --- | --- |
| Configuration | `~/.config/makebox/config.json` | `--config` or `MKBOX_CONFIG` |
| Journals and state | `/var/lib/mkbox/journals` | `--state-dir` or `MKBOX_STATE_DIR` |
| Configuration backups | `<file>.mkbox.bak.<timestamp>` | Created automatically before writes |
| Bundle keys | `mkbox-bundle.key` / `mkbox-bundle.pub` | `bundle keygen --out-dir` |
| Configuration snapshots | root: `/var/backups/makebox`; user: `~/.makebox/backups` | `backup create --dir` |

Common configuration commands:

```bash
mkbox config show
mkbox config get k8s.cni
mkbox config set k8s.cni calico
mkbox config set registry.mirrors 'https://a.example.com,https://b.example.com'
mkbox config set proxy.http 'http://127.0.0.1:7890'
```

Proxy passwords and GitHub proxy credentials are replaced with `REDACTED` by default in `plan`, `diff`, `runtime`, `report`, and `config show`.

## Security design

Safe by default, not patched afterwards. From the first file write, backup, atomic writes, credential redaction, and permission boundaries apply by default.

- **Back up first**: writes create `.mkbox.bak.<timestamp>` files; restores back up files again before overwriting.
- **Atomic writes**: temporary file plus `rename`, so an interrupted write cannot leave half-written configuration.
- **Merge, do not replace**: `daemon.json` is merged as a map, preserving unknown fields.
- **Dry-run and confirmation**: `--dry-run` prints the plan; non-TTY environments must pass `-y` explicitly.
- **Transactional journals**: `apply` snapshots managed files and only allows paths under `/etc`. Rollback rejects repeated runs and out-of-scope paths.
- **Credential redaction**: proxy and GitHub proxy passwords are hidden by default; private keys, journals, and reports use `0600`.
- **Supply-chain verification**: bundles use SHA-256, SPDX SBOMs, and Ed25519. OCI pulls hash while writing and reject blobs whose size or digest does not match.
- **No silent telemetry**: apart from explicit network probes, downloads, and registry pushes/pulls, mkbox does not upload host data.

## Documentation

- [Landing page](web/index.html) (static; open it directly or host it anywhere)
- [Command reference](docs/commands.md)
- [Architecture and design](docs/architecture.md)
- [Troubleshooting](docs/troubleshooting.md)

## Development and testing

```bash
make check          # gofmt + go vet + go test
make smoke          # build and run read-only / dry-run smoke tests
make strict-smoke   # rebuild artifacts and run the strict artifact suite
make build          # build bin/mkbox locally
make cross          # linux/amd64, linux/arm64, linux/arm(7)
make release        # cross-compile + source archive + checksums.txt
```

The strict smoke suite covers release artifact structure, static linking, source archives, the installer, dry-run safety, path traversal, journal rollback, reproducible bundle builds, signature verification, and a local OCI registry push/pull. End-to-end validation requires root and a virtual machine.

Issues and pull requests are welcome. Run `make check` after changes, and `make strict-smoke` when release artifacts are affected.

## Limitations

- Automated installation cannot cover every distribution and version. Use `--dry-run` first when package names or repositories differ.
- Public Docker Hub mirrors, `pkgs.k8s.io`, GitHub, and Rancher download endpoints may be affected by network conditions.
- `k8s preflight` disables swap and adjusts kernel parameters and firewall rules. Run `mkbox plan` first and apply changes in a maintenance window.
- `docker uninstall --purge` deletes `/var/lib/docker`; `k8s reset --all` deletes cluster data. Neither is recoverable.
- `apply` only asks for confirmation on irreversible actions; it does not uninstall packages or reset clusters automatically.
- Bundle private key custody is the user's responsibility. Production signing should happen offline or with hardware-backed keys, and only public keys should be distributed.
- Runtime coexistence checks are read-only and do not modify CRI sockets, cgroup drivers, or iptables backends.

## License

[MIT](LICENSE) © makebox contributors
