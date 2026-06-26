# Transform Sandbox Design

**Date:** 2026-06-26
**Branch:** slopfest
**Scope:** Timeout cap env var, memory limit env var, bubblewrap sandboxing

## Overview

Three hardening improvements to the transform execution path in `executor.go`:

1. `TRANSFORM_TIMEOUT` — server-wide hard cap on per-request timeouts
2. `TRANSFORM_MEMORY_LIMIT` — virtual address space limit for every transform process
3. Bubblewrap sandboxing — namespace isolation replacing the existing setuid drop

## Configuration

Two env vars are read once at startup in `main.go` and stored as package-level vars used by `execute()`.

| Env var | Type | Default | Meaning |
|---|---|---|---|
| `TRANSFORM_TIMEOUT` | int (seconds) | `30` | Hard cap: per-request `timeout` is clamped to this value; also used as default when `req.Timeout == 0` |
| `TRANSFORM_MEMORY_LIMIT` | int (bytes) | `134217728` (128 MiB) | Virtual address space limit (`RLIMIT_AS`) applied to every transform process |

Invalid or missing values fall back to defaults with a log warning.

## Timeout Cap

In `execute()`, the current logic:

```go
timeout := req.Timeout
if timeout <= 0 {
    timeout = 30
}
```

becomes:

```go
timeout := req.Timeout
if timeout <= 0 || timeout > timeoutCap {
    timeout = timeoutCap
}
```

where `timeoutCap` is the parsed `TRANSFORM_TIMEOUT` value. This means requests cannot opt into longer timeouts than the server allows, and the server default is the cap itself.

## Memory Limit

Applied via `ulimit -v` (RLIMIT_AS, in kilobytes) inside the busybox shell wrapper before exec'ing the interpreter. `memLimit` is stored in bytes; the conversion is `memLimit/1024`.

```go
shellCmd := fmt.Sprintf("ulimit -v %d && exec \"$@\"", memLimit/1024)
args := append(buildBwrapArgs(tmpDir), "/bin/sh", "-c", shellCmd, "--", interp, scriptPath)
```

When the limit is exceeded the process receives SIGKILL or gets ENOMEM; it exits non-zero and surfaces as `transform exited non-zero: ...` via the existing error path. No special-casing required.

## Bubblewrap Sandbox

### What changes

`execute()` replaces the direct interpreter invocation and `SysProcAttr.Credential` setuid drop with a bwrap-wrapped command:

```go
cmd := exec.CommandContext(ctx, "bwrap", buildBwrapArgs(tmpDir, interp, scriptPath)...)
cmd.SysProcAttr = &syscall.SysProcAttr{
    Rlimit: []syscall.Rlimit{{Type: syscall.RLIMIT_AS, Cur: memLimit, Max: memLimit}},
}
```

The `nobodyUID`/`nobodyGID` package vars and the `init()` block that resolved them are removed entirely.

### Sandbox arguments

```
--unshare-user          # new user namespace; runner maps to nobody inside
--uid 65534 --gid 65534 # appear as nobody:nogroup inside the sandbox
--unshare-pid           # new PID namespace
--unshare-ipc           # new IPC namespace
--unshare-uts           # new UTS namespace (hostname isolation)
--ro-bind /usr /usr     # interpreters + stdlib, read-only
--ro-bind /lib /lib     # musl libc (real dir on Alpine, not a symlink)
--proc /proc
--dev /dev
--tmpfs /tmp
--ro-bind /etc/resolv.conf /etc/resolv.conf
--ro-bind /etc/ssl/certs /etc/ssl/certs
--ro-bind /etc/php83 /etc/php83      # PHP config (php.ini etc.)
--bind <tmpDir> <tmpDir>             # script + output file, read-write
--new-session                        # no terminal hijacking
-- <interp> <scriptPath>
```

### Network

Network is **not** unshared. The sandbox inherits the container's network namespace, giving full internet access. Blocking access to local/private networks (RFC1918, loopback) is enforced at the Docker/host network level (e.g., iptables OUTPUT rules), not inside bwrap — unprivileged user namespaces cannot configure network rules.

### Error cases

| Scenario | Behaviour |
|---|---|
| `bwrap` not on PATH | `exec.CommandContext` returns error immediately → 422 with message |
| User namespaces unavailable | bwrap exits with clear message on stderr → 422 |
| Memory limit exceeded | Process killed → 422 `transform exited non-zero: Killed` |
| Timeout exceeded | Context cancelled → 422 `transform timed out after N seconds` |

## Dockerfile Changes

```dockerfile
# Remove:
RUN apk add --no-cache python3 nodejs php83 libcap && \
    ...
RUN setcap 'cap_setuid+ep cap_setgid+ep' /usr/local/bin/runner

# Becomes:
RUN apk add --no-cache python3 nodejs php83 bubblewrap && \
    ...
# (no setcap line)
```

`libcap` and `setcap` are removed. `bubblewrap` is added. The runner binary no longer holds any capabilities.

**Deployment requirement:** The host kernel must support unprivileged user namespaces (`kernel.unprivileged_userns_clone=1`, default on Linux 5.x+). If running in a hardened environment where this is disabled, bwrap will fail to start.

## Test Changes

`TestSubprocessRunsAsNobody` is updated:

- Remove the `os.Getuid() == 0` root gate (bwrap user namespaces work without root)
- Add `requireInterpreter(t, "bwrap")` guard
- Remove reference to `nobodyUID` package var (deleted); assert `uid == 65534` directly

All existing language tests exercise the full bwrap path and will catch sandbox misconfiguration implicitly.
