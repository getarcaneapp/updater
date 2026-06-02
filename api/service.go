// Package api exposes the standalone updater service.
package api

import (
	"log/slog"
	"sync"

	"github.com/getarcaneapp/updater/pkg/labels"
	"github.com/getarcaneapp/updater/types"
	"github.com/moby/moby/api/types/container"
)

// Service coordinates Docker image updates, container recreation, and host adapters.
type Service struct {
	config Config
	logger *slog.Logger

	statusMu           sync.RWMutex
	updatingContainers map[string]bool
	updatingProjects   map[string]bool
}

type updatePlan struct {
	record types.ImageUpdateRecord
	oldRef string
	newRef string
	oldIDs []string
	pulled bool
}

type restartPlan struct {
	cnt      container.Summary
	inspect  *container.InspectResponse
	newRef   string
	match    string
	explicit bool
	implicit bool
}

// NewService constructs an updater service.
func NewService(config Config) *Service {
	if config.LabelPolicy.IsUpdateDisabledFunc == nil {
		config.LabelPolicy = labels.DefaultLabelPolicy()
	}
	logger := config.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		config:             config,
		logger:             logger,
		updatingContainers: map[string]bool{},
		updatingProjects:   map[string]bool{},
	}
}
