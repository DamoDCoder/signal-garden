# 0015: Ship an image, but not a stack

- **Date:** 2026-08-25
- **Status:** Accepted
- **Refines:** [0011](0011-the-ui-is-a-separate-repository.md)

## Context

[0011](0011-the-ui-is-a-separate-repository.md) put the compose file in the client repository: the
dependency runs one way, the client needs a daemon and the daemon needs nothing from the client, so
the file describing both belongs with the component that has the dependency.

It did not say where the daemon's *image* comes from, and the client repository answered that on its
own by cloning this repository at a tag inside a Dockerfile and compiling it there. That worked and
was wrong in a way worth naming: it made a second repository responsible for knowing how this one is
built. A change to the Go version, the build flags, or the binary's runtime dependencies would have
been a change over there.

## Decision

**This repository ships the image. The client repository composes it.**

`Dockerfile` here builds `signalgardend`, and `task docker:build` produces it under
`signal-garden/signalgardend`. The compose file in app.signal-garden names that image and does not
build it.

The line is the same one 0011 drew, applied one level down: an artifact is owned by the repository
that knows how to make it, and a *stack* is owned by the repository that has the dependency.

**The image is built locally and never pushed.** There is no registry in this project. `docker:build`
writes to the local image store, compose reads from it with `pull_policy: never`, and a missing
image fails with "not found locally" rather than an authentication error against a registry nobody
configured. Publishing is a deployment concern, and deployment is not a milestone here yet.

**Two tags, and a label.** The version tag — `v0.7.0`, or `v0.7.0-dirty` from a working tree with
changes in it — is what a client would pin and is how a stale image is spotted. `:local` is a stable
name compose can default to without being edited on every bump. `org.opencontainers.image.version`
carries the real version either way, so a stack running `:local` can still be asked which contract
it was built from — which is what the client's `task up` does before it starts anything.

`:local` is applied **only when the image is built for this machine's architecture**. It is a moving
tag, and a cross-architecture build that claimed it would leave compose starting an amd64 daemon on
an arm64 host — under emulation if the runtime allows it, and not at all if it does not — with a
tag that had silently moved as the only clue. A cross build gets its version tag and nothing else.

## Why The Image Copies A Binary Instead Of Compiling One

A multi-stage build that compiles inside Docker would make `docker build` self-contained. It would
also compile the daemon a second way, with a second Go version, in a second environment — and the
binary that `task check` tested would not be the binary that ran.

So `docker:build` depends on `build:docker`, and the image is a copy. The cost is that a bare
`docker build .` fails without the cross-compile having run first; `task docker:build` runs both in
order and is the supported way in.

## Two Architectures, Which Is The Part That Bites

A developer machine builds for two platforms that are not interchangeable:

| Task | Output | Platform on an Apple Silicon Mac |
| --- | --- | --- |
| `task build` | `bin/signalgardend` | `darwin/arm64` |
| `task build:docker` | `bin/linux_arm64/signalgardend` | `linux/arm64` |

Same instruction set, different operating system. A darwin binary in a Linux container fails with
`exec format error`, which names the symptom and not the cause, and it is a genuinely confusing
half-hour if the two builds share an output path and whichever ran last decides whether the image
works.

They do not share one. Host binaries stay in `bin/`, container binaries go to
`bin/<os>_<arch>/`, and `docker build --platform` is passed the same pair the binary was built for,
so the image's metadata cannot disagree with the bytes inside it. `ARCH=amd64` overrides the
architecture, which is what building an image for an x86 machine from an Apple Silicon one needs.

`CGO_ENABLED=0` is part of the same problem. A dynamically linked binary built against the host's
libc starts fine here and fails on Alpine, which is musl and has no loader for it.

## What Would Revisit This

- A registry, at which point `:local` and `pull_policy: never` become the local special case rather
  than the only case.
- A second image from this repository — a load generator at M3, say — which would make `IMAGE_NAME`
  a per-target variable rather than a repository-wide one.
- Reproducible builds mattering enough that compiling inside the image is worth the divergence from
  the binary the tests ran against.
