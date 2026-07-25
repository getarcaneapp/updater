// Package updater keeps Docker containers running current images: it pulls
// newer images, recreates the containers using them, and restarts whatever
// depended on those containers.
//
// # Getting started
//
// Everything runs through a [Service]. The zero [Config] is valid and produces
// a service backed entirely by the local Docker environment:
//
//	service, err := updater.New(updater.Config{})
//	if err != nil {
//		return err
//	}
//	defer service.Close()
//
//	result, err := service.UpdateContainer(ctx, containerID, updater.Options{})
//
// [Service.UpdateContainer] updates one container by ID.
// [Service.ApplyPending] works through every update recorded in the configured
// [PendingStore], and restarts the containers still running the images it
// replaced.
//
// # Configuring
//
// Each field of [Config] is a seam the host application can take over. Leave a
// field nil and the updater supplies a Docker-backed default for it — or, for
// the purely optional ones, simply skips the behavior. So a caller can replace
// just the pieces they care about:
//
//	service, err := updater.New(updater.Config{
//		PendingStore: myStore,       // where pending updates come from
//		Notifier:     myNotifier,    // told about each successful update
//		SelfUpdater:  myUpgrader,    // handles the container we run in
//	})
//
// [LabelPolicy] decides which containers are in scope, using container labels.
// It merges per field, so overriding one behavior keeps the defaults for the
// rest. See [DefaultLabelPolicy].
//
// # Compose and self-update
//
// Containers carrying Docker Compose labels are grouped by project and updated
// through the configured [ProjectUpdater] rather than being recreated
// individually, then verified to no longer run the old image. Containers the
// [LabelPolicy] marks as self-update targets — including the one named by
// Config.SelfContainerID — are handed to the configured [SelfUpdater] instead,
// last in the run, since updating them may stop this process.
//
// # Building
//
// This module uses the experimental encoding/json/v2 package. Build and test it
// with GOEXPERIMENT=jsonv2 set.
package updater
