# Firecracker feasibility spike (2026-10-03)

**Verdict: PASS.** Nested KVM on this machine is fast enough to use Firecracker microVMs as the per-trial isolation boundary.

## Environment
- Host: WSL2 (Ubuntu 22.04, kernel 5.15.167.4), AMD Ryzen 5 5500U, 12 vCPU, 7.4 GiB RAM, Windows 11 (build 26200). No `.wslconfig` was created or changed.
- `/dev/kvm` access via the `kvm` group (this shell needed `sg kvm -c ...` until WSL is fully restarted).
- Firecracker v1.17.0 (sha256 verified against the published checksum), guest kernel 6.1.186 (official CI artifact), Ubuntu 24.04 rootfs (CI artifact) plus the host's node v22.23.2 and pi 0.85.1 copied in.
- Artifacts live in `spike/firecracker/` and are disposable. Rebuild with the steps in `run_vm.py` / `spike-init.sh`.

## Criteria and results

| # | Criterion | Result |
|---|---|---|
| 1 | Boot a microVM in under ~3 s | Kernel up in 1.2 s guest time; init reached at 2.0-2.5 s host-measured; whole VM lifetime incl. all tests 3-5 s |
| 2 | vsock round trip guest -> host UDS | OK, 1.6-3.1 ms round trip |
| 3 | `node` and `pi --version` in guest | OK: node 139 ms, `pi --version` 0.86 s, `pi --help` 0.76 s |
| 4 | Two concurrent 1.5 GiB guests | OK: ~2.2 s boot each; combined Firecracker RSS 97 MB (lazy allocation); host stayed responsive |

## Findings that change the design
- **cgroup v2 is complete in the guest:** controllers `cpuset cpu io memory hugetlb pids`; child cgroup has `memory.peak`, `memory.events`, `memory.max`, `pids.max`, `cpu.max`, `cpu.stat`. A 300 MB allocation under `memory.max=100M` was OOM-killed (exit 137, `oom_kill 1`, `memory.peak=104857600`). This removes the WSL cgroup-v2 prerequisite and the 5.15 `memory.peak` workaround.
- **Real uids work:** a setuid-2000 binary run by uid 1000 ran as euid 2000, and uid 1000 got `Permission denied` reading it. No `newuidmap`/subuid needed.
- **Overlayfs works as real root** (whiteouts and new files land in the upper dir).
- **vsock** replaces the loopback-to-Unix-socket bridge from the original design.
- Guest sees `hypervisor` in `/proc/cpuinfo` (eval-awareness tell; the realism profile can't remove it).

## Caveats / follow-ups
- **Do not copy the rootfs per trial.** Copying 697 MB took 1.8-8.7 s. Use one read-only base image per VM plus a small per-trial writable layer (tmpfs or sparse disk).
- **Snapshot restore is untested.** Needed for the warm-pool start-time metric.
- **seccomp user-notify inside the guest is untested.** The kernel config enables `SECCOMP_FILTER`; verify the notify path in the exec-log unit.
- **The test image was built without root,** so files are owned by uid 1000. The real image build needs root ownership (build as root, in a VM, or with fakeroot).
- Guest kernel 6.18.51 is also available from the same CI and could replace 6.1.186 if newer kernel features are wanted.
