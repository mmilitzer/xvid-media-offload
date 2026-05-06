# Media Local Offload

> Reclaim local disk space without breaking your CMS.

Media Local Offload is a lightweight Linux tool that automatically frees disk space from large MP4 video files while keeping them fully visible to your content management system. It is designed especially for sites running **Elevated-X** and other CMS platforms that require media files to remain present in the local filesystem even when the live site serves them from the **Xvid MediaHub CDN**.

## What it does for you

Video hosting bills are split two ways: cloud storage for delivery and local disk for the CMS. Over time, generated MP4 files pile up on the web server and eat through expensive local storage. Deleting them breaks the CMS. Moving them elsewhere breaks the CMS. Symbolic links and mount tricks are fragile and hard to maintain.

Media Local Offload solves this with a much simpler approach:

- **The file stays right where it is.** Your CMS still sees the original path and the original file size.
- **The first part of the file is preserved** so MP4 metadata such as duration and resolution remains readable, and initial preview frames continue to work.
- **The bulk of the file is transparently deallocated** using Linux hole punching, instantly freeing most of the disk blocks.
- **The full video can be restored on demand** from Xvid MediaHub cloud storage whenever it is needed again.

For site operators and FTP/SFTP users, restoring a file is as simple as deleting a small companion marker:

```text
filename.mp4.offloaded
```

Delete that `.offloaded` file and the full video is automatically downloaded back to local disk. No command line knowledge required.

---

## Installation

Pre-built Linux amd64 binaries are available from the GitHub Actions CI pipeline:

1. Open the [Actions](../../actions) tab.
2. Select the most recent **CI** workflow run.
3. Scroll down to the **Artifacts** section.
4. Download `media-offload-linux-amd64`.

Place the binary somewhere in your `$PATH`, for example `~/bin/media-offload`, and make it executable:

```bash
chmod +x ~/bin/media-offload
```

If `~/bin` is not in your path yet, add it in your shell profile:

```bash
export PATH="$HOME/bin:$PATH"
```

The tool is a single static binary with no CGo dependency and no runtime requirements beyond a modern Linux kernel.

---

## Configuration

Media Local Offload reads a YAML configuration file. A minimal example looks like this:

```yaml
scan_roots:
  - /home/html/site_root/content

candidate_globs:
  - "**/4k/*.mp4"

minimum_age: "30d"
```

### Required fields

| Field | Description |
|-------|-------------|
| `scan_roots` | List of directory trees to scan for managed media folders. |
| `candidate_globs` | Glob patterns that decide which MP4 files are eligible for offloading. Patterns are matched against paths relative to each scan root. |
| `minimum_age` | Minimum age a file must reach before it can be offloaded. Accepts human-friendly durations such as `30d`, `2w`, `6mo`, `1y`, or standard Go durations like `720h`. |

### Optional fields

| Field | Default | Description |
|-------|---------|-------------|
| `marker_filename` | `.htaccess` | Name of the marker file that identifies a managed content folder. |
| `marker_depth` | `1` | How many directory levels below `scan_roots` the tool looks for marker files. Use `2` for layouts like `content/group1/set1/.htaccess`. |
| `keep_prefix_bytes` | `52428800` (50 MB) | How many bytes to preserve at the start of each offloaded file so MP4 metadata remains readable. |
| `scan_interval` | `24h` | How often the daemon performs a full filesystem scan. |
| `restore_workers` | `4` | Number of parallel restore workers the daemon uses. |
| `download_timeout` | `6h` | Global HTTP timeout for downloading restored files. Long enough for large, slow downloads. |
| `database_path` | *(none)* | Path to an optional SQLite database used as a fallback for remote file IDs and as an audit log. |
| `lock_file` | *(next to config)* | Path to the advisory lock file that prevents the daemon and CLI commands from running simultaneously. |

### Credentials

The tool discovers Xvid MediaHub API credentials automatically by walking upward from each scan root and looking for a `cmsinclude.ini.php` file. The file should contain an `[xvid]` section with `APP_CLIENT_ID` and `APP_CLIENT_SECRET`:

```ini
[xvid]
APP_CLIENT_ID = "your-client-id"
APP_CLIENT_SECRET = "your-client-secret"
```

No credentials need to be written into the config file itself.

---

## Standalone usage

The binary provides four commands: `scan`, `shrink`, `restore`, and `daemon`.

### `scan` — preview candidates

The `scan` command is read-only. It reports which files are currently eligible for offloading without changing anything on disk:

```bash
media-offload scan --config ./config.yaml
```

Add `--verbose` to see per-file skip reasons and detailed errors:

```bash
media-offload scan --config ./config.yaml --verbose
```

### `shrink` — offload files

Preview what would happen:

```bash
media-offload shrink --config ./config.yaml --dry-run
```

Apply the offload (you will be prompted for confirmation unless `--yes` is passed):

```bash
media-offload shrink --config ./config.yaml --apply --yes
```

Files are only modified when all safety checks pass: the folder must have a valid marker file, the file must match a candidate glob, it must be older than `minimum_age`, and its remote file ID must be known.

### `restore` — restore offloaded files

Scan for sparse files that no longer have an `.offloaded` marker and restore them from cloud storage:

```bash
media-offload restore --config ./config.yaml --dry-run
media-offload restore --config ./config.yaml
```

By default four parallel workers are used. Adjust with `--workers`.

### `daemon` — continuous operation

The daemon combines everything into a single background service:

- Periodic scans find new offload candidates and shrink them automatically.
- inotify watches detect when `.offloaded` markers are deleted and trigger restores immediately.
- A local SQLite database (when configured) keeps fallback metadata and an audit trail.
- On startup the daemon reconciles the database against filesystem reality, cleaning up stale records.

Run the daemon manually:

```bash
media-offload daemon --config ~/media-offload/config.yaml
```

---

## Daemon setup with systemd

The daemon is designed to run as an unprivileged user. Below is a complete, copy-paste ready systemd service unit.

Create the service file at `/etc/systemd/system/media-offload.service`:

```ini
[Unit]
Description=Media Local Offload daemon
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=myuser
Group=myuser
ExecStart=/home/myuser/bin/media-offload daemon --config /home/myuser/media-offload/config.yaml
Restart=on-failure
RestartSec=10

# Capabilities
# Preserving the original file modification time after hole punching requires
# the process to either own the MP4 file or hold CAP_FOWNER. If the daemon
# user is not the file owner but has group write access, add CAP_FOWNER here.
AmbientCapabilities=CAP_FOWNER
CapabilityBoundingSet=CAP_FOWNER

# Security hardening
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/home/html/site_root/content /home/myuser/media-offload

[Install]
WantedBy=multi-user.target
```

Adjust `User`, `Group`, `ExecStart`, and `ReadWritePaths` to match the owner of your content directories and the location of your config and optional database.

**Permission note:** Linux only allows a process to change the timestamps of a file it does not own if it has the `CAP_FOWNER` capability. Group write permission on the file is **not** sufficient. If the daemon runs as a user that is not the owner of the MP4 files but has group write access, you must grant `CAP_FOWNER` via the systemd unit (as shown above) or by setting the capability on the binary itself with `setcap cap_fowner=+ep /path/to/media-offload`. If `CAP_FOWNER` is missing, offloading still succeeds but the original modification time is lost and an error is logged.

Then create the configuration directory, place your `config.yaml` inside it, and start the service:

```bash
mkdir -p ~/media-offload
# edit ~/media-offload/config.yaml

systemctl daemon-reload
systemctl enable --now media-offload
systemctl status media-offload
```

Logs are available via journald:

```bash
journalctl -u media-offload -f
```

Because the daemon holds an advisory file lock, running a manual `shrink` or `restore` command while the daemon is active will be safely refused (dry-run scans are still allowed).

**Note:** The daemon reads the configuration file once at startup. If you change `config.yaml`, restart the service to apply the changes:

```bash
systemctl restart media-offload
```

---

## How it works

### Hole punching and sparse files

Instead of deleting a file, the tool keeps it in place and uses Linux hole punching (`fallocate(FALLOC_FL_PUNCH_HOLE | FALLOC_FL_KEEP_SIZE)`) to deallocate disk blocks from a configurable offset to the end of the file. The logical file size reported by `stat()` remains unchanged, but the actual allocated disk space drops to roughly the size of the preserved prefix.

The preserved prefix (default 50 MB) is large enough to keep MP4 metadata boxes intact so that duration, resolution, and initial preview frames remain readable.

This technique is supported on common Linux filesystems including ext4, XFS, and Btrfs. The tool detects unsupported filesystems safely and skips offloading rather than damaging files.

### Marker files and folder ownership

Each content folder generated by the Xvid system contains a `.htaccess` marker file. The marker proves that the folder belongs to the platform and contains the remote file identifiers needed for restore operations.

A marker is valid when its first line is exactly:

```text
#Xvid AutoGraph content protection system.
```

The marker also maps local MP4 filenames to remote file IDs via Apache `RewriteRule` patterns. The tool parses these rules, matches them against actual files on disk, and uses the corresponding `file_id` values to sign download URLs when a restore is requested.

### The `.offloaded` companion marker

After a file is offloaded, the tool creates a sibling marker file:

```text
filename.mp4.offloaded
```

The presence of this marker tells the tool that the file has already been processed. Removing the marker signals that the full file should be restored. This simple mechanism makes restore operations accessible to anyone with FTP/SFTP access, with no need for shell commands.

### Filesystem-first design

The filesystem is the primary source of truth. The daemon can rebuild useful state at any time by scanning the configured directory trees, reading marker files, detecting `.offloaded` companions, spotting sparse MP4 files, and cleaning up interrupted restore temp files.

The optional SQLite database acts as a fallback and audit log, but the system does not depend on it for normal operation. If the database is lost, a scan plus marker parsing is enough to continue offloading and restoring files.

### Candidate eligibility

A file is considered an offload candidate only when **all** of the following are true:

- It lives under a configured `scan_root`.
- Its parent folder contains a valid marker file.
- It is a regular file with a `.mp4` extension.
- Its path matches one of the configured `candidate_globs`.
- Its modification time is older than `minimum_age`.
- The marker file contains a matching remote file ID for it.
- It does not already have a sibling `.offloaded` marker.
- It is not already sparse.
- Its size is larger than twice `keep_prefix_bytes`.

### Restore lifecycle

When an `.offloaded` marker disappears, the daemon:

1. Resolves the remote file ID from the marker file (or from the database as a fallback).
2. Checks available disk space.
3. Signs a temporary download URL using the credentials found in the content tree.
4. Downloads the full file to a temporary path such as `filename.mp4.restore.<uuid>.tmp`.
5. Verifies the downloaded size matches the expected size.
6. Copies the original file permissions to the temporary file.
7. Atomically renames the temporary file over the sparse stub.
8. Fsyncs the parent directory.
9. Updates the file modification time to the current time so it is not immediately offloaded again.
10. Removes the database record if one exists.

If there is not enough disk space to restore, the daemon recreates the `.offloaded` marker and leaves the file in the offloaded state.

---

## Architecture and design principles

- **No mounts.** The tool works directly on the existing local filesystem. No FUSE, OverlayFS, bind mounts, or other kernel layering tricks are used.
- **Minimal persistent state.** Only files that were actually modified by the tool are tracked. Candidate discovery is always done from a fresh filesystem scan.
- **Idempotent operations.** Running the same command twice produces the same end state. Offloading a file that is already sparse is a no-op.
- **Crash recoverable.** Interrupted restores leave behind temp files with a predictable suffix (`.restore.*.tmp`). The next restore attempt cleans them up automatically.
- **Conservative with disk space.** Restores check available space before downloading. If space is tight, the file stays offloaded.
- **Unprivileged by default.** The daemon can run as the same user that owns the content files, requiring no root access.

---

## Building from source

The project is written in Go and builds into a single static binary.

Requirements:

- Go 1.22 or newer
- Linux (ext4, XFS, or Btrfs recommended)

```bash
git clone https://github.com/mmilitzer/xvid-media-offload.git
cd xvid-media-offload
CGO_ENABLED=0 go build -o media-offload ./cmd/media-offload
```

Run the tests:

```bash
go test ./...
go vet ./...
```

---

## Built-in safety principles

- Never modify a file unless its folder has a valid marker file.
- Never modify a file unless its remote file ID is known.
- Never modify files in dry-run mode.
- Never treat all MP4 files as ours.
- Never rely on the database as the only source of truth.
- Prefer filesystem reality over cached state.
- Keep restore and offload operations atomic where possible.
- Be conservative when disk space is low.
