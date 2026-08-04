# Changelog

All notable changes to this project are documented here.

The project has not reached its first versioned release. Until then, work is grouped under `Unreleased`.

## [Unreleased]

### Added

- Project pack imported from the ideas cradle: roadmap, architecture, contracts, event model, local development, and feedback plan.

### Changed

- M0 no longer requires protobuf or gRPC. A single process has no service boundary to separate, so M0 defines its seams as Go interfaces and M1 introduces the wire contracts when a second process makes them load-bearing.
- M0 projection is a CLI rather than a React screen, which keeps the milestone free of the Node toolchain.

## Commit Guidance

Use a short imperative subject with a small scope:

```text
<type>(<scope>): <imperative summary>
```

Useful types are `docs`, `feat`, `test`, `perf`, `refactor`, `chore`, and `fix`. Keep each commit focused on one coherent batch. Add a body when the decision or tradeoff would be difficult to infer from the subject.
