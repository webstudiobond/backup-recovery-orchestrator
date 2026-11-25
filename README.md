# Backup & Recovery Orchestrator (bro)

bro orchestrates point-in-time backups, integrity verification, offsite replication, and disaster recovery for Docker Compose stacks on Debian-based Linux distributions. It requires systemd, LVM2, Docker Engine with Docker Compose v2, GNU tar, and zstd.

## Motivation

Production hosts across the [webstudiobond](https://github.com/webstudiobond), [underhax](https://github.com/underhax), and [cdua-org](https://github.com/cdua-org) ecosystems run a minimal set of system utilities, isolating all workloads inside Docker Compose stacks under strict Discretionary Access Control. Stack data resides on LVM volumes mounted with nodev, nosuid, and noexec, while containers operate with dropped capabilities and shell-less or scratch images. Edge traffic routes through [angie-docker-compose](https://github.com/webstudiobond/angie-docker-compose) to application stacks—such as [stalwart-docker](https://github.com/webstudiobond/stalwart-docker) and [wordpress-docker](https://github.com/webstudiobond/wordpress-docker)—backed by relational, document, key-value, embedded, or flat-file storage engines.

bro was built to automate backups, disaster recovery, and DAC policy enforcement across this locked-down environment. It coordinates online streaming dumps for relational databases, captures atomic LVM snapshots of any stack directory or embedded store without service downtime, audits and applies DAC permissions directly on live hosts, synchronizes Angie virtual hosts and TLS certificates with backend services, and preserves exact numeric UIDs and GIDs during restoration.

## Requirements

- Operating System: Linux (Debian, Ubuntu, or derivatives) with systemd.
- Kernel: `dm_snapshot` module loaded.
- Storage: LVM2 (`lvcreate`, `lvremove`, `lvs`). Stack data directories must reside on an LVM logical volume with unallocated space in the volume group for snapshot allocation.
- Container Runtime: Docker Engine with the Docker Compose v2 plugin (`docker compose`).
- System Utilities: GNU `tar`, `zstd`, `mount`, `umount`, `mountpoint` (util-linux), `useradd`, `groupadd` (shadow/passwd).
- Optional:
  - `rclone`: required only when using `rclone` destination types or remote trash cleanup.
  - Reverse Proxy (Angie): reverse-proxy container stack (`reverse_proxy_stack` in `config.json` with `reverse_proxy_provider` in `conf.d/<stack>.json`), required only when managing virtual hosts, streams, or ACME TLS certificates.

## Capabilities

Backup & Archival
- Point-in-time filesystem capture via temporary LVM snapshots without container downtime.
- Online database streaming with live zstd compression, sourcing credentials from Docker secrets or host files.
- Synchronized capture of reverse-proxy virtual hosts, stream configs, and ACME TLS certificates.
- Declarative pre-dump, pre-snapshot, and post-snapshot lifecycle hooks.
- Self-backup of orchestrator configurations, stack profiles, and systemd units.

Disaster Recovery
- Archive verification against embedded SHA-256 checksum manifests prior to extraction.
- Host account validation and conflict-free provisioning with exact numeric UIDs and GIDs.
- Clean-slate file restoration with path traversal protection.
- Reverse-proxy configuration restoration (`deploy_reverse_proxy`, `--reverse-proxy*`) with syntax validation and automatic rollback on failure.
- Step-level filtering to execute or skip specific restore actions.

Storage & DAC
- Replication to local directories, systemd mounts, FUSE filesystems, and rclone remotes.
- Local and remote retention enforcement with automated pruning and remote cleanup.
- Standalone Discretionary Access Control auditing and enforcement on live stacks.

## Installation

### Package Repository (Recommended)

Add the official repository to receive seamless updates via standard system package managers.

**Debian / Ubuntu:**

```bash
sudo install -m 0755 -d /etc/apt/keyrings
curl -fsSL https://webstudiobond.github.io/backup-recovery-orchestrator/webstudiobond.asc | sudo tee /etc/apt/keyrings/webstudiobond.asc > /dev/null
echo "deb [signed-by=/etc/apt/keyrings/webstudiobond.asc] https://webstudiobond.github.io/backup-recovery-orchestrator/deb stable main" | sudo tee /etc/apt/sources.list.d/bro.list
sudo apt update
sudo apt install bro
```

### Automated Script

Run the automated installer script to detect your platform architecture, verify SHA256 checksums from GitHub Releases, and install the package:

```bash
curl -fSsL https://raw.githubusercontent.com/webstudiobond/backup-recovery-orchestrator/main/scripts/install.sh | sudo bash
```

### Manual Download & Verification

Download the latest package from [Releases](https://github.com/webstudiobond/backup-recovery-orchestrator/releases), verify its SHA256 digest from the GitHub API, and install:

```bash
curl -fSsLO https://github.com/webstudiobond/backup-recovery-orchestrator/releases/latest/download/bro_<version>_<arch>.deb
EXPECTED_HASH=$(curl -fSs https://api.github.com/repos/webstudiobond/backup-recovery-orchestrator/releases/latest | jq -r '.assets[] | select(.name == "bro_<version>_<arch>.deb") | .digest' | sed 's/sha256://')
echo "${EXPECTED_HASH}  bro_<version>_<arch>.deb" | sha256sum -c -
sudo dpkg -i bro_<version>_<arch>.deb
```

For manual binary installation without the package manager, download the release archive (`.tar.gz`), extract the binary, and place it into `/usr/bin/bro`:

```bash
chown root:root /usr/bin/bro
chmod 0500 /usr/bin/bro
install -d -m 0700 /etc/bro /etc/bro/conf.d /etc/bro/profiles
```

### Configuration Setup

Reference templates in `/usr/share/doc/bro/examples/` (or repository `examples/`) illustrate the configuration structure, syntax, and inheritance patterns:

```text
/etc/bro/
├── config.json                      # Global orchestrator configuration
├── profiles/                        # Optional: shared base profiles
│   └── <profile_name>.json          # Common excludes, database dumps, and DAC rules
└── conf.d/                          # Per-stack declarations
    └── <stack_name>.json            # Docker Compose stack backup and DAC specification
```

#### Global Configuration

`/etc/bro/config.json` is the primary orchestrator configuration file. It defines host-wide operational parameters, including the backup storage root, LVM volume and snapshot settings, the active `reverse_proxy_stack` reference, retention policies, and replication destinations.

Copy the template from examples and customize it for your host:

```bash
cp /usr/share/doc/bro/examples/etc/bro/config.json /etc/bro/config.json
chmod 0400 /etc/bro/config.json
```

#### Profiles & Stack Declarations

Profiles under `profiles/` are optional and exist to eliminate duplication when running multiple instances of the same service type (for example, hosting multiple WordPress sites on one server). When multiple stacks share identical file exclusion lists, database dump mechanics, or DAC permissions, extract that common logic into a profile. The repository and package provide [`wordpress.json`](examples/etc/bro/profiles/wordpress.json) under `/usr/share/doc/bro/examples/etc/bro/profiles/wordpress.json` as a reference example demonstrating shared rules for WordPress instances. If your host runs distinct, non-repeating workloads, you do not need to create any profiles.

Create a JSON declaration under `conf.d/` for each Docker Compose stack you want `bro` to manage. Stacks can inherit a profile using `"extends": "<profile_name>"`—for example, [`site1.json`](examples/etc/bro/conf.d/site1.json) extends `wordpress`, inheriting all database dump rules and file exclusions while declaring only site-specific attributes (the host user UID/GID and domain mappings). Stacks without shared logic define their complete specification directly (reference templates: [`angie.json`](examples/etc/bro/conf.d/angie.json), [`stalwart.json`](examples/etc/bro/conf.d/stalwart.json)).

Set strict ownership and permissions:

```bash
chown -R root:root /etc/bro
find /etc/bro -type f -name "*.json" -exec chmod 0400 {} +
```

### Systemd Service & Timer Setup

Automated execution requires the `bro-full.service` and `bro-full.timer` units (installed automatically by the Debian package). The `/usr/share/doc/bro/examples/etc/systemd/system/` directory also provides reference mount units demonstrating how to integrate on-demand storage destinations:

```text
/etc/systemd/system/
├── bro-full.service                 # Core service executing orchestrator backups
├── bro-full.timer                   # Core timer triggering scheduled execution
├── <mount-point>.mount              # Optional: mount unit for offsite storage destinations
└── <mount-point>.automount          # Optional: on-demand automount trigger
```

#### Service Unit (bro-full.service)

The `bro-full.service` unit runs a complete backup cycle (`bro backup --all`) across all enabled stacks, creates temporary LVM snapshots, streams databases, and replicates archives to configured storage targets. It runs as a low-priority oneshot background task to avoid impacting live container workloads. Reference template: [`bro-full.service`](examples/etc/systemd/system/bro-full.service).

1) Host configuration for kernel modules:
The unit enforces kernel sandboxing (`ProtectKernelModules=yes`), dropping `CAP_SYS_MODULE` and filtering `@module` system calls. Because `bro` cannot dynamically load kernel modules inside this restricted sandbox, the `dm_snapshot` module required for LVM snapshot operations must be loaded by the host operating system at boot (pre-configured automatically when installing via package):

```bash
printf 'dm_snapshot\n' > /etc/modules-load.d/lvm-snapshot.conf
chown root:root /etc/modules-load.d/lvm-snapshot.conf
chmod 0400 /etc/modules-load.d/lvm-snapshot.conf
systemctl restart systemd-modules-load.service
```

2) Configuration adjustments in the unit:
The service operates under strict filesystem isolation (`ProtectSystem=strict`, `ProtectHome=read-only`), intentionally restricting write access across the host. Writable locations are explicitly allowlisted via `ReadWritePaths`:

```ini
ReadWritePaths=/backups /mnt /run /etc/lvm -/home/matrix/tuwunel/data
```

Do not modify `/mnt`, `/run`, or `/etc/lvm`—these are fixed infrastructure paths required for snapshot mounting, runtime locking, and LVM metadata operations.

Adjust the remaining paths according to your environment:
- Replace `/backups` if using a custom `backup_root` in `/etc/bro/config.json`.
- The path `-/home/matrix/tuwunel/data` demonstrates how to grant write access when lifecycle hooks manipulate files on the host—as illustrated in [`matrix.json`](examples/etc/bro/conf.d/matrix.json), where pre- and post-snapshot hooks create and remove checkpoint files under that directory. The leading `-` prefix marks the path as optional: systemd skips it silently without error if the directory does not exist on the machine. You can safely leave it as-is, remove it, or append your own stack paths using the same `-` prefix.

#### Timer Unit (bro-full.timer)

The `bro-full.timer` unit triggers automated periodic execution of `bro-full.service`. Reference template: [`bro-full.timer`](examples/etc/systemd/system/bro-full.timer).

Configuration adjustments in the unit:

```ini
OnCalendar=*-*-* 03:00:00
RandomizedDelaySec=10m
Persistent=true
```

- `OnCalendar`: defines the execution schedule (default is daily at 03:00).
- `RandomizedDelaySec`: applies a random delay window (default 10 minutes) to prevent simultaneous I/O bursts across multiple servers or remote storage targets.
- `Persistent=true`: ensures systemd catches up and executes missed backups immediately if the host was powered off during the scheduled window.

#### Storage Mount Units (Optional)

If offsite replication destinations rely on systemd-managed FUSE or network filesystems, create corresponding mount and automount units. The templates [`mnt-cloud-primary.mount`](examples/etc/systemd/system/mnt-cloud-primary.mount) and [`mnt-cloud-primary.automount`](examples/etc/systemd/system/mnt-cloud-primary.automount) demonstrate how to establish on-demand mounting for remote storage targets.

#### Unit Activation

Set ownership and permissions on the deployed units (if installing manually):

```bash
chown root:root /etc/systemd/system/bro-full.service /etc/systemd/system/bro-full.timer
chmod 0400 /etc/systemd/system/bro-full.service /etc/systemd/system/bro-full.timer
```

Reload the systemd daemon and activate the timer:

```bash
systemctl daemon-reload
systemctl enable --now bro-full.timer
```
