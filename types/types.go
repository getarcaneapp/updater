// Package types contains the public data contracts for the updater module.
package types

import (
	"context"
	"strings"
	"time"
)

const (
	// UpdateTypeDigest identifies an image update where the tag is unchanged but the digest changed.
	UpdateTypeDigest = "digest"
	// UpdateTypeTag identifies an image update where the target tag changes.
	UpdateTypeTag = "tag"
)

const (
	// ResourceTypeImage is the result resource type for image pull/check work.
	ResourceTypeImage = "image"
	// ResourceTypeContainer is the result resource type for container update work.
	ResourceTypeContainer = "container"
	// ResourceTypeProject is the result resource type for compose project update work.
	ResourceTypeProject = "project"
)

const (
	// StatusChecked indicates a resource was checked.
	StatusChecked = "checked"
	// StatusUpdated indicates a resource was updated.
	StatusUpdated = "updated"
	// StatusRestarted indicates a resource was restarted because a dependency changed.
	StatusRestarted = "restarted"
	// StatusSkipped indicates a resource was skipped.
	StatusSkipped = "skipped"
	// StatusFailed indicates a resource failed to update.
	StatusFailed = "failed"
	// StatusUpToDate indicates a resource is already up to date.
	StatusUpToDate = "up_to_date"
	// StatusUpdateAvailable indicates an update is available.
	StatusUpdateAvailable = "update_available"
)

// Options configures an updater run.
type Options struct {
	Type        string   `json:"type,omitempty"`
	ResourceIDs []string `json:"resourceIds,omitempty"`
	Force       bool     `json:"forceUpdate,omitempty"`
	DryRun      bool     `json:"dryRun,omitempty"`
}

// ResourceResult represents the result of an update operation on one resource.
type ResourceResult struct {
	ResourceID      string            `json:"resourceId"`
	ResourceName    string            `json:"resourceName,omitempty"`
	ResourceType    string            `json:"resourceType"`
	Status          string            `json:"status"`
	UpdateAvailable bool              `json:"updateAvailable,omitempty"`
	UpdateApplied   bool              `json:"updateApplied,omitempty"`
	OldImages       map[string]string `json:"oldImages,omitempty"`
	NewImages       map[string]string `json:"newImages,omitempty"`
	Error           string            `json:"error,omitempty"`
	Details         map[string]any    `json:"details,omitempty"`
}

// Result represents a complete updater run.
type Result struct {
	Success    bool             `json:"success,omitempty"`
	Checked    int              `json:"checked"`
	Updated    int              `json:"updated"`
	Restarted  int              `json:"restarted,omitempty"`
	Skipped    int              `json:"skipped"`
	Failed     int              `json:"failed"`
	StartTime  string           `json:"startTime,omitempty"`
	EndTime    string           `json:"endTime,omitempty"`
	Duration   string           `json:"duration"`
	Items      []ResourceResult `json:"items"`
	ActivityID *string          `json:"activityId,omitempty"`
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
	UpdateType     string
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

// HistoryRecord is a caller-agnostic persisted updater history row.
type HistoryRecord struct {
	ID              string
	ResourceID      string
	ResourceType    string
	ResourceName    string
	Status          string
	StartTime       time.Time
	EndTime         *time.Time
	UpdateAvailable bool
	UpdateApplied   bool
	OldImages       map[string]string
	NewImages       map[string]string
	Error           *string
	Details         map[string]any
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
	ResourceType string
	Metadata     map[string]any
}

// LabelPolicy controls update labels, self-update labels, and swarm detection.
type LabelPolicy struct {
	IsUpdateDisabledFunc   func(map[string]string) bool
	IsSelfUpdateTargetFunc func(map[string]string) bool
	IsAgentFunc            func(map[string]string) bool
	IsServerFunc           func(map[string]string) bool
	IsSwarmTaskFunc        func(map[string]string) bool
	StopSignalFunc         func(map[string]string) string
}

// IsUpdateDisabled reports whether labels opt the container out of updates.
func (p LabelPolicy) IsUpdateDisabled(labels map[string]string) bool {
	return p.IsUpdateDisabledFunc != nil && p.IsUpdateDisabledFunc(labels)
}

// IsSelfUpdateTarget reports whether labels require host self-update handling.
func (p LabelPolicy) IsSelfUpdateTarget(labels map[string]string) bool {
	return p.IsSelfUpdateTargetFunc != nil && p.IsSelfUpdateTargetFunc(labels)
}

// IsAgent reports whether labels identify an agent self-update target.
func (p LabelPolicy) IsAgent(labels map[string]string) bool {
	return p.IsAgentFunc != nil && p.IsAgentFunc(labels)
}

// IsServer reports whether labels identify a server self-update target.
func (p LabelPolicy) IsServer(labels map[string]string) bool {
	return p.IsServerFunc != nil && p.IsServerFunc(labels)
}

// IsSwarmTask reports whether labels identify a Docker Swarm task container.
func (p LabelPolicy) IsSwarmTask(labels map[string]string) bool {
	return p.IsSwarmTaskFunc != nil && p.IsSwarmTaskFunc(labels)
}

// StopSignal returns the configured stop signal for a container.
func (p LabelPolicy) StopSignal(labels map[string]string) string {
	if p.StopSignalFunc == nil {
		return ""
	}
	return p.StopSignalFunc(labels)
}

// ContextHook is a convenience adapter for context-only callbacks.
type ContextHook func(context.Context) error
