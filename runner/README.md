# Runner Scripts

This directory contains scripts required to compile executables using self-hosted runners.

## GitHub Actions Workflows

### 1. 'Build and Publish' Workflow

See: [`../.github/workflows/build_and_publish.yml`](../.github/workflows/build_and_publish.yml)

This workflow compiles native (non-pure Go) executables for the following Linux distributions and architectures:

- **Distributions:**
  - Debian 10, 11, 12 (libc: GLIBC)
  - Ubuntu 20.04, 22.04, 24.04 (libc: GLIBC)
  - Alpine Linux (using musl-gcc)

- **Architectures:**
  - amd64, i386
  - arm64, armv7

### 2. PureGo Cross-Builds

The workflow also cross-compiles pure Go executables for:

- **Operating Systems:**
  - FreeBSD, NetBSD, OpenBSD
  - macOS (except i386/armv7)
  - Windows

- **Architectures:**
  - amd64, i386
  - arm64, armv7

### 3. 'GoReleaser' Workflow

See:
- [`../.github/workflows/.goreleaser_test_.yml`](../.github/workflows/.goreleaser_test_.yml)
- [`../.goreleaser.yaml`](../.goreleaser.yaml)

GoReleaser compiles all pure Go executables on a single self-hosted machine.

---

For more details, refer to the respective workflow and configuration files.