package updater

// UpdateType distinguishes the two kinds of image update the updater applies.
type UpdateType string

const (
	// UpdateTypeDigest identifies an image update where the tag is unchanged but the digest changed.
	UpdateTypeDigest UpdateType = "digest"
	// UpdateTypeTag identifies an image update where the target tag changes.
	UpdateTypeTag UpdateType = "tag"
)

// ResourceType names the kind of thing a ResourceResult describes.
type ResourceType string

const (
	// ResourceTypeImage is the result resource type for image pull/check work.
	ResourceTypeImage ResourceType = "image"
	// ResourceTypeContainer is the result resource type for container update work.
	ResourceTypeContainer ResourceType = "container"
	// ResourceTypeProject is the result resource type for compose project update work.
	ResourceTypeProject ResourceType = "project"
)

// ResourceStatus is the outcome recorded for one resource in a run. It is
// distinct from Status, which reports the work a Service has in flight.
type ResourceStatus string

const (
	// StatusChecked indicates a resource was checked.
	StatusChecked ResourceStatus = "checked"
	// StatusUpdated indicates a resource was updated.
	StatusUpdated ResourceStatus = "updated"
	// StatusRestarted indicates a resource was restarted because a dependency changed.
	StatusRestarted ResourceStatus = "restarted"
	// StatusSkipped indicates a resource was skipped.
	StatusSkipped ResourceStatus = "skipped"
	// StatusFailed indicates a resource failed to update.
	StatusFailed ResourceStatus = "failed"
	// StatusUpToDate indicates a resource is already up to date.
	StatusUpToDate ResourceStatus = "up_to_date"
	// StatusUpdateAvailable indicates an update is available.
	StatusUpdateAvailable ResourceStatus = "update_available"
)
