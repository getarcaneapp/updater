package updater_test

import (
	"context"
	"fmt"
	"log"

	"go.getarcane.app/updater"
)

// The zero Config gives a service backed entirely by the local Docker
// environment.
func ExampleNew() {
	service, err := updater.New(updater.Config{})
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		if err := service.Close(); err != nil {
			log.Print(err)
		}
	}()

	result, err := service.UpdateContainer(context.Background(), "my-container-id", updater.Options{})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("updated %d of %d containers in %s\n", result.Updated, result.Checked, result.Duration())
}

// ApplyPending works through the updates a PendingStore reports, then restarts
// the containers still running the images it replaced.
func ExampleService_ApplyPending() {
	store := updater.NewMemoryPendingStore(updater.ImageUpdateRecord{
		ID:         "sha256:old-image-id",
		Repository: "nginx",
		Tag:        "1.27",
		HasUpdate:  true,
		UpdateType: updater.UpdateTypeDigest,
	})

	service, err := updater.New(updater.Config{PendingStore: store})
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		if err := service.Close(); err != nil {
			log.Print(err)
		}
	}()

	// DryRun reports what would change without pulling or recreating anything.
	result, err := service.ApplyPending(context.Background(), updater.Options{DryRun: true})
	if err != nil {
		log.Fatal(err)
	}
	for _, item := range result.Items {
		fmt.Printf("%s %s: %s -> %s\n", item.ResourceType, item.Status, item.OldImage, item.NewImage)
	}
}
