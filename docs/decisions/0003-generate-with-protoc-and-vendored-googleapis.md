# 0003: Generate with protoc and vendored googleapis protos

- **Date:** 2026-08-05
- **Status:** Accepted

## Context

M1 introduces protobuf, gRPC, and the generated REST gateway, per [0001](0001-defer-grpc-to-m1.md). Generating the REST routes named in [contracts.md](../contracts.md) needs `google/api/annotations.proto`, which is not part of protoc's bundled includes. Something has to supply it, and [local-development.md](../local-development.md) requires that generated code be reproducible from pinned tool versions with no cloud credentials.

## Options Considered

1. **buf.** Resolves googleapis from the BSR, manages plugin versions, lints and checks breaking changes. Another toolchain binary to install, and dependency resolution reaches the network by default.
2. **protoc, with the two googleapis protos vendored into `third_party/` and plugins pinned as Go tool dependencies.** No extra binary beyond protoc; generation is offline and hermetic.
3. **No annotations: `generate_unbound_methods`.** Routes get derived from method names instead of declared.

## Decision

Option 2. `proto/` holds the contract, `third_party/google/api/` holds the two vendored Apache-2.0 protos, and `go.mod` tool directives pin `protoc-gen-go`, `protoc-gen-go-grpc`, and `protoc-gen-grpc-gateway`. The `proto` task builds those plugins from the pinned versions and runs protoc against them. Generated Go lands in `internal/gen/` and is committed.

## Evidence

Option 3 was rejected first: derived routes would not be the routes contracts.md specifies, and `POST /v1/runs/{run_id}:pause` is not expressible without annotations. The contract would have quietly become whatever the generator chose.

Between buf and protoc, the deciding factor is that the annotation protos are two stable files that have not changed meaningfully in years. Vendoring them costs 13KB and removes both a binary install and a network dependency from the `proto` task. buf's real advantages — breaking-change detection and lint — matter when a contract has external consumers negotiating compatibility; this one has a React client in the same repository. Adopting buf later is a task-runner change, because the `.proto` files themselves are unchanged by this decision.

Generated code is committed so a clean checkout builds and tests without protoc installed. Only regenerating needs the toolchain.

## What Would Revisit This

- A second repository or a non-Go client starts consuming the contract, which makes breaking-change detection worth a toolchain.
- The contract grows dependencies beyond `google/api`, at which point vendoring stops being two files and starts being dependency management.
