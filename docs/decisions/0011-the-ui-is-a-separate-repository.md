# 0011: The UI is a separate repository; this one owns the contract

- **Date:** 2026-08-22
- **Status:** Accepted
- **Supersedes:** [0003](0003-generate-with-protoc-and-vendored-googleapis.md)

## Context

[0003](0003-generate-with-protoc-and-vendored-googleapis.md) chose protoc with vendored googleapis protos over buf, and one of its stated reasons was that buf's breaking-change detection "matters when a contract has external consumers negotiating compatibility; this one has a React client in the same repository."

The React client now lives in [app.signal-garden](https://github.com/DamoDCoder/app.signal-garden). That premise is false, so the record has to be re-decided rather than quietly left standing on a fact that changed.

The split raises three questions the single-repository layout never had to answer: who defines a garden, how the client gets types, and where `docker compose up` lives when the stack spans two checkouts.

## Decision

**This repository owns the contract.** `proto/signal/garden/v1/garden.proto` is the single definition of a run, a garden, an event, and a projection frame. Generated Go stays committed here. The client repository consumes the contract and never describes a garden of its own — a second definition would be free to drift until someone noticed in a browser, which is the failure [0010](0010-one-contract-for-both-transports.md) exists to prevent and would reintroduce across a repository boundary where it is harder to see.

**Compose lives in the UI repository.** The dependency runs one way: the client needs a daemon to talk to, and the daemon needs nothing from the client. A compose file describes the whole stack, so it belongs with the component that has the dependency rather than the one being depended on. Practically it also means the daemon repository stays runnable with `task serve` and nothing else, and the stack that needs orchestrating is orchestrated where someone is already working on it.

**Generation stays protoc with vendored protos.** The consumer moved, but nothing else 0003 weighed did: the annotation protos are still two stable files, vendoring them still removes a binary install and a network dependency, and generation still runs offline from versions pinned in `go.mod`.

## What Changed About The buf Question

0003 dismissed buf's breaking-change detection because the only consumer was in-tree. That is now the strongest argument for it: a contract with a consumer in another repository can break that consumer without a single test failing here.

It is still not enough to switch, for two reasons. Both repositories belong to one person and move together, so a break is discovered on the next `task check` in the client rather than in production. And the client has not chosen a code generator yet — adopting buf before knowing whether the client generates with `protoc-gen-es`, ts-proto, or an OpenAPI spec would be picking a toolchain to solve a problem that has not been described.

What replaces it for now is narrower and cheaper: the contract is versioned with this repository's tags, so the client pins a tag rather than tracking `main`.

## What Would Revisit This

- A second consumer, or a consumer this project does not control, at which point breaking-change detection stops being a courtesy to oneself.
- The client repository choosing a generator that wants buf anyway, which would make the cost of adopting it close to zero.
- The contract needing to be published as an artifact rather than read from a git tag.
