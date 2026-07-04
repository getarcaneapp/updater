<div align="center">

# Arcane Updater

Portable Docker auto-update orchestration for Go applications.

<a href="https://github.com/getarcaneapp/updater/actions/workflows/ci.yml"><img src="https://github.com/getarcaneapp/updater/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
<a href="https://pkg.go.dev/go.getarcane.app/updater"><img src="https://pkg.go.dev/badge/go.getarcane.app/updater.svg" alt="Go Reference"></a>
<a href="https://github.com/getarcaneapp/updater/blob/main/LICENSE"><img src="https://img.shields.io/badge/license-BSD--3--Clause-blue.svg" alt="License"></a>

</div>

Arcane Updater is the standalone Go module behind Arcane's Docker auto-updater flow. It provides the update engine, Docker client integration, image pulling, registry digest checks, Docker image matching, Compose-aware recreation, self-update routing, and reusable helper packages.

The module can be used directly in non-Arcane Go applications. Arcane-specific persistence, activity, and notification behavior is still adapter-driven, but Docker access and ordinary container updates work out of the box.

## How it works

Using Arcane Updater is a small Docker update flow:

1. Create an `api.Service` with `api.Config`.
2. Use the built-in Docker client, image puller, registry digest resolver, memory pending store, and Docker Compose updater, or override them in config.
3. Call `ApplyPending` to apply stored image update records, or `UpdateContainer` to update one container directly.
4. The service normalizes image references, skips excluded or disabled containers, checks digests when a resolver is configured, pulls the target image, and recreates matching standalone containers.
5. Compose containers are grouped by Compose labels, recreated through Docker Compose, then verified against the old image ID.
6. Containers identified as self-update targets are handled by `SelfUpdater` instead of the standalone recreate path.
7. Results are returned as `types.Result`, and optional recorders receive run, event, and notification payloads.

The updater does not own scheduling, durable persistence, user notifications, or application self-upgrade behavior. Those can be added by providing adapters.

## Getting started

```sh
go get go.getarcane.app/updater@latest
```

```go
svc := api.NewDefaultService()

result, err := svc.UpdateContainer(ctx, "container-id", types.Options{})
```

For pending image records:

```go
store := api.NewMemoryPendingStore(types.ImageUpdateRecord{
	ID:         "sha256:old-image-id",
	Repository: "nginx",
	Tag:        "1.27",
	HasUpdate:  true,
	UpdateType: types.UpdateTypeDigest,
})

svc := api.NewService(api.Config{PendingStore: store})

result, err := svc.ApplyPending(ctx, types.Options{})
```

## Package layout

- `api`: public updater service and adapter interfaces.
- `types`: stable public DTOs for options, results, status, records, labels, events, and notifications.
- `pkg/refs`: image reference normalization.
- `pkg/digest`: digest parsing and local/remote digest comparison.
- `pkg/match`: image and container matching helpers.
- `pkg/labels`: default updater, Compose, self-update, and swarm label behavior.
- `pkg/registry`: registry HTTP digest and rate-limit helpers.
- `pkg/deps`: container dependency sorting.
- `pkg/utils`: shared Docker, Compose label, registry, and compatibility utilities.
- `pkg/logs`: message-only log file setup helpers.

## Development

```sh
just format all
go test ./...
just lint all
```

## License

Arcane Updater is released under the BSD 3-Clause License.
