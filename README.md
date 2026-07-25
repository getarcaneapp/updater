<div align="center">

# Arcane Updater

Portable Docker auto-update orchestration for Go applications.

<a href="https://github.com/getarcaneapp/updater/actions/workflows/ci.yml"><img src="https://github.com/getarcaneapp/updater/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
<a href="https://pkg.go.dev/go.getarcane.app/updater"><img src="https://pkg.go.dev/badge/go.getarcane.app/updater.svg" alt="Go Reference"></a>
<a href="https://github.com/getarcaneapp/updater/blob/main/LICENSE"><img src="https://img.shields.io/badge/license-BSD--3--Clause-blue.svg" alt="License"></a>

</div>

Arcane Updater is the standalone Go module behind Arcane's Docker auto-updater. It pulls newer images, recreates the containers running them, and restarts whatever depended on those containers — with Compose awareness, registry digest checks, and a path for containers that must update themselves.

It works out of the box against the local Docker environment. Persistence, notifications, and self-upgrade behavior stay adapter-driven, so a host application plugs in only the parts it owns.

> [!IMPORTANT]
> This module is in early development. The API is not stable and may change without notice.

> [!NOTE]
> This module uses the experimental `encoding/json/v2` package. Build and test with `GOEXPERIMENT=jsonv2` set.

## Install

```sh
go get go.getarcane.app/updater@latest
```

## Quick start

Everything runs through a `Service`. The zero `Config` is valid:

```go
import "go.getarcane.app/updater"

service, err := updater.New(updater.Config{})
if err != nil {
	return err
}
defer service.Close()

result, err := service.UpdateContainer(ctx, containerID, updater.Options{})
```

To work through recorded pending updates instead of one named container:

```go
store := updater.NewMemoryPendingStore(updater.ImageUpdateRecord{
	ID:         "sha256:old-image-id",
	Repository: "nginx",
	Tag:        "1.27",
	HasUpdate:  true,
	UpdateType: updater.UpdateTypeDigest,
})

service, err := updater.New(updater.Config{PendingStore: store})
if err != nil {
	return err
}
defer service.Close()

result, err := service.ApplyPending(ctx, updater.Options{})
```

`ApplyPending` applies each pending update and then restarts the containers still running the images it replaced. `Options{DryRun: true}` reports what would change without touching anything.

## Configuring

Every `Config` field is a seam. Leave one nil and the updater supplies a Docker-backed default — or, for the purely optional ones, skips that behavior entirely. Override only what you own:

```go
service, err := updater.New(updater.Config{
	// Replace a default.
	DockerClientProvider: updater.NewDockerClientProvider(client.WithHost("tcp://docker-proxy:2375")),
	PendingStore:         myStore,

	// Add optional behavior.
	Notifier:      myNotifier,   // told about each successful update
	EventRecorder: myEvents,     // receives lifecycle events
	SelfUpdater:   myUpgrader,   // handles the container we run in

	// Restrict which containers are in scope.
	LabelPolicy: updater.LabelPolicy{
		IsUpdateDisabledFunc: func(l map[string]string) bool { return l["updates"] == "off" },
	},

	OperationTimeout: 30 * time.Second,
})
```

`LabelPolicy` merges per field: overriding `IsUpdateDisabledFunc` above keeps the default behavior for self-update, agent, server, swarm, and stop-signal detection. See `DefaultLabelPolicy`.

`Service.Close` shuts down the Docker client `New` created. If you supplied your own `DockerClientProvider`, its lifecycle stays yours and `Close` is a no-op.

## How it works

1. Resolve a pullable image reference for each in-scope container, skipping image IDs and digest-pinned references — re-pulling those can never yield a newer image.
2. Skip containers the `LabelPolicy` disables, containers the host excludes via `SettingsProvider`, and Swarm tasks.
3. Check the registry digest, so an image that has not actually changed is not needlessly recreated (`Options{Force: true}` bypasses this).
4. Pull the target image.
5. Recreate the container — through the `ProjectUpdater` for Compose services (grouped by project, then verified to no longer run the old image), or by a direct stop/create/start with rollback for standalone containers.
6. Restart containers that depend on what just changed, in dependency order.
7. Hand self-update targets to the `SelfUpdater`, last, since updating them may stop the calling process.

Scheduling, durable persistence, and user-facing notifications are not the module's job; supply adapters for those.

## Package layout

| Package | Contents |
| --- | --- |
| `updater` | The `Service`, `Config`, the port interfaces, and the result types. Everything a host application normally needs. |
| `updater/refs` | Image reference normalization and pullability checks. |
| `updater/digest` | OCI digest parsing and canonicalization. |
| `updater/labels` | The Arcane container labels and the predicates that read them. |
| `updater/registry` | Registry HTTP digest and pull-rate-limit lookups. |

Implementation details live under `internal/` and are not part of the public API.

## Migrating from v0.6.x

v0.7.0 collapses `api` and `types` into the module root, so one import replaces two.

| Before | After |
| --- | --- |
| `api.NewService(cfg)` | `updater.New(cfg)`, which now returns `(*Service, error)` |
| `api.NewDefaultService()` | `updater.New(updater.Config{})` |
| `api.Service`, `api.Config`, the port interfaces | same names in `updater` |
| `types.Options`, `types.Result`, `types.Status`, … | same names in `updater` |
| `api.ResolvePullableImageRef` | `refs.PullableImageRef`, without the second return |
| `api.UpdateStandaloneContainer` | unexported; use `UpdateContainer` |
| `pkg/digest.RemoteResolver` (`GetImageDigest`) | `updater.RegistryDigestResolver` (`ImageDigest`) |
| `pkg/labels.DefaultLabelPolicy` | `updater.DefaultLabelPolicy` |
| `pkg/labels.GetStopSignal` | `labels.StopSignal` |
| `pkg/registry.NewRegistryHTTPClient` | `registry.NewHTTPClient` |
| `pkg/{refs,digest,labels,registry}` | `{refs,digest,labels,registry}` — drop the `pkg/` |
| `pkg/{match,deps,utils}`, digest `Checker`/`RefIDCache` | moved to `internal/`; no longer public |
| `pkg/logs` | removed |
| `types.HistoryRecord`, `types.ContextHook` | removed; nothing consumed them |

Reshaped data types:

- `Result.StartTime`/`EndTime` are now `time.Time`; the pre-formatted `Duration` string is now the `Duration()` method. `ActivityID` is gone.
- `ResourceResult.OldImages`/`NewImages` maps are now the `OldImage`/`NewImage` strings they always held.
- `Options.Type` and `Options.ResourceIDs` are gone; nothing read them.
- `ResourceType`, `ResourceStatus`, and `UpdateType` are now named string types. The constant *values* are unchanged, so persisted data stays valid.

`New` also fixes a trap: `LabelPolicy` now merges per field, so overriding one func no longer silently drops the defaults for the others.

## Development

```sh
just format all
just test
just lint
```

## License

Arcane Updater is released under the BSD 3-Clause License.
