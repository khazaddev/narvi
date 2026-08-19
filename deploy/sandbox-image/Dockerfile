# deploy/sandbox-image/Dockerfile is the real build definition
# internal/app/sessionactor/dispatch.go's own defaultBaseImage doc comment
# names as not-yet-existing ("today a placeholder tag; its real build
# definition is where these land") — §27.7 ("toolchain in images", Step
# 74) is what actually lands it, since nothing before this Step needed a
# concrete image to point ports.CreateSpec.Image at.
#
# Scope, stated as plainly as §27.7 itself states it: "a content addition
# to the base sandbox image, no new architecture... That is the entire
# design; stating more would be manufacturing complexity." This file
# builds the ONE base image every cold boot and every per-repo prebuild
# (§19) starts FROM — not a per-repo-set prebuild itself (§19's own build
# service layers repo clones/dependency caches on TOP of this, external
# to this repository, per that section's own "external, opaque-to-this-
# repo build service" boundary). Nothing here is provider-specific: the
# provider (Modal today, §4.1) only ever needs an image REFERENCE
# (ports.CreateSpec.Image) — how that reference gets built and pushed to
# wherever the provider pulls images from is this file's own job, wired
# into a real registry/CI pipeline as a later, separate operational
# concern (out of THIS Step's scope, same as every other "external build
# service" boundary this plan already draws).
#
# Versions are pinned throughout (§27.7: "Versions pinned and visible via
# the boot fingerprint's existing image digest", §5.3 — sandbox-agent's
# own boot fingerprint already logs NARVI_IMAGE_DIGEST first thing,
# internal/sandboxagent/boot/config.go; nothing new needed there, a
# content change to this file simply produces a new digest). Every ARG
# below is the ONE place its own version lives — bump here, rebuild,
# done.

# Must track this repo's own go.mod "go" directive -- go.mod's own tool
# directive (Go's own module-scoped tool-dependency mechanism, backing
# `go tool go-jsonschema`/`go tool sqlc`, Makefile) requires a matching
# or newer Go toolchain to even parse.
FROM golang:1.26-bookworm AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# CGO_ENABLED=0: a static binary, the same posture §1's own "static
# binary shipped into sandbox images" repo-layout comment already
# documents for cmd/sandbox-agent.
RUN CGO_ENABLED=0 go build -trimpath -o /out/sandbox-agent ./cmd/sandbox-agent

FROM debian:bookworm-slim

# --- §27.7's own named additions, each pinned ---

# ripgrep: installed from its own pinned GitHub release tarball (Debian's
# own bookworm apt repo carries a much older version by default, and a
# release tarball pins exactly, the same way every other tool below
# does) rather than the OS package manager, so this ARG is the single
# source of truth for the shipped version regardless of which Debian
# release this base image is ever rebased onto.
ARG RIPGREP_VERSION=14.1.1
# typescript-language-server + typescript (§27.7: "typescript-language-
# server (+ typescript)") — installed via npm, which needs Node.js first;
# Node's own LTS line, pinned.
ARG NODE_MAJOR=20
ARG TYPESCRIPT_LANGUAGE_SERVER_VERSION=4.3.3
ARG TYPESCRIPT_VERSION=5.6.3
# Playwright + Chromium (§27.7: "installed with its OS deps, pinned") —
# the npm package version also pins the exact Chromium build `playwright
# install` fetches; the two must move together, never independently.
ARG PLAYWRIGHT_VERSION=1.48.2
# Docker CLI/engine (§27.7: "the Docker CLI/engine binaries... daemon
# dormant unless §27.5's flag is set") — Docker's own official static
# binary tarball, pinned; the daemon this ships is never started by this
# image itself (internal/sandboxagent/boot.RunDocker, §27.5, is the ONLY
# thing that ever execs dockerd, and only when SessionConfig.Docker is
# true).
ARG DOCKER_VERSION=27.3.1
# §27.4's three cloud exec-credential plugins (§27.7: "the three cloud
# exec-credential plugins") — the exact three §27.4 names: the AWS CLI
# itself (whose `aws eks get-token` subcommand IS the EKS exec plugin,
# not a separate binary), gke-gcloud-auth-plugin (installed via a pinned
# gcloud SDK release), and kubelogin (Azure workload-identity mode).
ARG AWSCLI_VERSION=2.19.4
ARG GCLOUD_VERSION=502.0.0
ARG KUBELOGIN_VERSION=0.2.4

# git: not named in §27.7's own bullet list, but load-bearing and worth
# recording explicitly here rather than leaving it an implicit apt
# transitive dependency — internal/sandboxagent/gitclone shells out to a
# real `git` binary for every clone/sync this codebase does (§3.4), and
# has since Step 13, long before this Step's own toolchain additions.
RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates curl unzip xz-utils git \
    && rm -rf /var/lib/apt/lists/*

# --- ripgrep ---
RUN ARCH="$(dpkg --print-architecture)" && \
    case "$ARCH" in \
        amd64) RG_ARCH=x86_64-unknown-linux-musl ;; \
        arm64) RG_ARCH=aarch64-unknown-linux-gnu ;; \
        *) echo "unsupported architecture: $ARCH" >&2; exit 1 ;; \
    esac && \
    curl -fsSL "https://github.com/BurntSushi/ripgrep/releases/download/${RIPGREP_VERSION}/ripgrep-${RIPGREP_VERSION}-${RG_ARCH}.tar.gz" \
        -o /tmp/ripgrep.tar.gz && \
    tar -xzf /tmp/ripgrep.tar.gz -C /tmp && \
    install -m 0755 "/tmp/ripgrep-${RIPGREP_VERSION}-${RG_ARCH}/rg" /usr/local/bin/rg && \
    rm -rf /tmp/ripgrep*

# --- Node.js (a prerequisite for typescript-language-server/typescript/
# Playwright below, not itself one of §27.7's named additions) ---
RUN curl -fsSL "https://deb.nodesource.com/setup_${NODE_MAJOR}.x" | bash - && \
    apt-get install -y --no-install-recommends nodejs && \
    rm -rf /var/lib/apt/lists/*

# --- typescript-language-server + typescript ---
RUN npm install -g \
        "typescript-language-server@${TYPESCRIPT_LANGUAGE_SERVER_VERSION}" \
        "typescript@${TYPESCRIPT_VERSION}"

# --- Playwright + Chromium, with its OS deps ---
# `playwright install --with-deps chromium` is Playwright's own
# documented mechanism for installing exactly the OS packages ITS pinned
# browser build needs (this varies by Playwright version and OS release,
# which is precisely why it is delegated to Playwright's own installer
# rather than this Dockerfile guessing at an apt package list of its
# own) -- chromium only (§27.7 names no other browser), never the
# firefox/webkit siblings this same CLI could also fetch.
RUN npm install -g "playwright@${PLAYWRIGHT_VERSION}" && \
    npx playwright install --with-deps chromium

# --- Docker CLI/engine binaries (dockerd itself never started here) ---
RUN ARCH="$(dpkg --print-architecture)" && \
    case "$ARCH" in \
        amd64) DOCKER_ARCH=x86_64 ;; \
        arm64) DOCKER_ARCH=aarch64 ;; \
        *) echo "unsupported architecture: $ARCH" >&2; exit 1 ;; \
    esac && \
    curl -fsSL "https://download.docker.com/linux/static/stable/${DOCKER_ARCH}/docker-${DOCKER_VERSION}.tgz" \
        -o /tmp/docker.tgz && \
    tar -xzf /tmp/docker.tgz -C /tmp && \
    install -m 0755 /tmp/docker/docker /usr/local/bin/docker && \
    install -m 0755 /tmp/docker/dockerd /usr/local/bin/dockerd && \
    install -m 0755 /tmp/docker/containerd /usr/local/bin/containerd && \
    install -m 0755 /tmp/docker/containerd-shim-runc-v2 /usr/local/bin/containerd-shim-runc-v2 && \
    install -m 0755 /tmp/docker/runc /usr/local/bin/runc && \
    rm -rf /tmp/docker /tmp/docker.tgz

# --- §27.4's three cloud exec-credential plugins ---
RUN ARCH="$(dpkg --print-architecture)" && \
    case "$ARCH" in \
        amd64) AWS_ARCH=x86_64 ;; \
        arm64) AWS_ARCH=aarch64 ;; \
        *) echo "unsupported architecture: $ARCH" >&2; exit 1 ;; \
    esac && \
    curl -fsSL "https://awscli.amazonaws.com/awscli-exe-linux-${AWS_ARCH}-${AWSCLI_VERSION}.zip" \
        -o /tmp/awscliv2.zip && \
    unzip -q /tmp/awscliv2.zip -d /tmp && \
    /tmp/aws/install && \
    rm -rf /tmp/awscliv2.zip /tmp/aws

RUN ARCH="$(dpkg --print-architecture)" && \
    curl -fsSL "https://dl.google.com/dl/cloudsdk/channels/rapid/downloads/google-cloud-cli-${GCLOUD_VERSION}-linux-${ARCH}.tar.gz" \
        -o /tmp/gcloud.tar.gz && \
    tar -xzf /tmp/gcloud.tar.gz -C /opt && \
    /opt/google-cloud-sdk/install.sh --quiet --path-update=false --usage-reporting=false && \
    /opt/google-cloud-sdk/bin/gcloud --quiet components install gke-gcloud-auth-plugin && \
    ln -s /opt/google-cloud-sdk/bin/gke-gcloud-auth-plugin /usr/local/bin/gke-gcloud-auth-plugin && \
    rm -rf /tmp/gcloud.tar.gz

RUN ARCH="$(dpkg --print-architecture)" && \
    curl -fsSL "https://github.com/Azure/kubelogin/releases/download/v${KUBELOGIN_VERSION}/kubelogin-linux-${ARCH}.zip" \
        -o /tmp/kubelogin.zip && \
    unzip -q /tmp/kubelogin.zip -d /tmp && \
    install -m 0755 "/tmp/bin/linux_${ARCH}/kubelogin" /usr/local/bin/kubelogin && \
    rm -rf /tmp/kubelogin.zip /tmp/bin

# --- sandbox-agent itself ---
COPY --from=builder /out/sandbox-agent /usr/local/bin/sandbox-agent

ENTRYPOINT ["/usr/local/bin/sandbox-agent"]
