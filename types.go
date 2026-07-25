package updater

import (
	"strings"
	"time"
)

// Options configures an updater run.
type Options struct {
	// Force applies updates even when the digest check says the image is
	// already current.
	Force bool `json:"forceUpdate,omitempty"`
	// DryRun reports what would be updated without pulling or recreating
	// anything.
	DryRun bool `json:"dryRun,omitempty"`
}

// ResourceResult represents the result of an update operation on one resource.
type ResourceResult struct {
	ResourceID      string         `json:"resourceId"`
	ResourceName    string         `json:"resourceName,omitempty"`
	ResourceType    ResourceType   `json:"resourceType"`
	Status          ResourceStatus `json:"status"`
	UpdateAvailable bool           `json:"updateAvailable,omitempty"`
	UpdateApplied   bool           `json:"updateApplied,omitempty"`
	OldImage        string         `json:"oldImage,omitempty"`
	NewImage        string         `json:"newImage,omitempty"`
	Error           string         `json:"error,omitempty"`
	Details         map[string]any `json:"details,omitempty"`
}

// Result represents a complete updater run.
type Result struct {
	Success   bool             `json:"success,omitempty"`
	Checked   int              `json:"checked"`
	Updated   int              `json:"updated"`
	Restarted int              `json:"restarted,omitempty"`
	Skipped   int              `json:"skipped"`
	Failed    int              `json:"failed"`
	StartTime time.Time        `json:"startTime,omitzero"`
	EndTime   time.Time        `json:"endTime,omitzero"`
	Items     []ResourceResult `json:"items"`
}

// Duration reports how long the run took. It is zero until the run finishes.
func (r Result) Duration() time.Duration {
	if r.StartTime.IsZero() || r.EndTime.IsZero() {
		return 0
	}
	return r.EndTime.Sub(r.StartTime)
}

// Status reports resources that are actively being updated by this service instance.
type Status struct {
	UpdatingContainers int      `json:"updatingContainers"`
	UpdatingProjects   int      `json:"updatingProjects"`
	ContainerIDs       []string `json:"containerIds"`
	ProjectIDs         []string `json:"projectIds"`
}

// ImageUpdateRecord is a pending image update known to a caller-provided store.
type ImageUpdateRecord struct {
	ID             string
	Repository     string
	Tag            string
	HasUpdate      bool
	UpdateType     UpdateType
	CurrentVersion string
	LatestVersion  *string
	CurrentDigest  *string
	LatestDigest   *string
	CheckTime      time.Time
	LastError      *string
}

// NeedsUpdate reports whether the record indicates a pending update.
func (i ImageUpdateRecord) NeedsUpdate() bool {
	return i.HasUpdate
}

// IsDigestUpdate reports whether the update is digest-based.
func (i ImageUpdateRecord) IsDigestUpdate() bool {
	return i.UpdateType == UpdateTypeDigest
}

// IsTagUpdate reports whether the update is tag-based.
func (i ImageUpdateRecord) IsTagUpdate() bool {
	return i.UpdateType == UpdateTypeTag
}

// ImageRef returns the current image reference represented by the record.
func (i ImageUpdateRecord) ImageRef() string {
	repo := strings.TrimSpace(i.Repository)
	tag := strings.TrimSpace(i.Tag)
	if repo == "" || tag == "" {
		return ""
	}
	return repo + ":" + tag
}

// NewImageRef returns the image reference the updater should pull for the record.
func (i ImageUpdateRecord) NewImageRef() string {
	repo := strings.TrimSpace(i.Repository)
	if repo == "" {
		return ""
	}
	if i.IsTagUpdate() && i.LatestVersion != nil && strings.TrimSpace(*i.LatestVersion) != "" {
		return repo + ":" + strings.TrimSpace(*i.LatestVersion)
	}
	return i.ImageRef()
}

// ComposeProject identifies a compose project known to the host application.
type ComposeProject struct {
	ID   string
	Name string
}

// SelfUpdateTarget describes a container that should be handled by a host self-updater.
type SelfUpdateTarget struct {
	ContainerID   string
	ContainerName string
	InstanceType  string
	Labels        map[string]string
	// NewImageRef is the resolved image reference the self-updater should
	// upgrade the container to. Empty when the updater could not resolve one.
	NewImageRef string
}

// Notification describes a successful container update notification.
type Notification struct {
	ContainerID   string
	ContainerName string
	ImageRef      string
	OldImage      string
	NewImage      string
}

// Event describes a generic updater event for host applications.
type Event struct {
	Phase        string
	Severity     string
	ResourceID   string
	ResourceName string
	ResourceType ResourceType
	Metadata     map[string]any
}
