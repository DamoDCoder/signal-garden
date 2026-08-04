# 0001: Defer protobuf and gRPC to M1

- **Date:** 2026-08-04
- **Status:** Accepted

## Context

The ideas-cradle pack made protobuf-first gRPC the default service contract for every Go project, and M0's exit criteria required that "gRPC service methods can be exercised locally." M0 is specified as a single process with an in-memory event bus and no external dependencies.

## Options Considered

1. **Protobuf and gRPC from the first commit.** Matches the cradle convention exactly. Costs proto codegen, a gateway, and pinned tool versions before any simulation rule exists.
2. **Go interfaces at M0, protobuf at M1.** Defines the same seams without the wire format. M1 introduces protobuf when the control service, processor, and projection gateway become separate processes.
3. **Drop gRPC entirely, use REST throughout.** Simplest, but abandons a deliberate learning goal of the project.

## Decision

Option 2. M0 defines its boundaries as Go interfaces whose method shapes match the service definitions in [contracts.md](../contracts.md). M1 introduces protobuf, gRPC, and the generated REST gateway.

## Evidence

A single process has no service boundary to separate. gRPC's value is a typed contract across a process boundary, plus generated clients and forward-compatible evolution. None of those apply until a second process exists, so at M0 the codegen toolchain is scaffolding around an interface Go already expresses natively.

## What Would Revisit This

- M1 boundaries land in a shape the M0 interfaces cannot express, which would mean the seams were drawn wrong and the translation is not mechanical.
- The project gains a second language client before M1, which would make a shared wire contract load-bearing earlier than expected.
