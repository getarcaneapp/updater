package api

import (
	"context"

	"github.com/getarcaneapp/updater/pkg/deps"
	"github.com/getarcaneapp/updater/pkg/utils"
)

type composeGroup struct {
	projectName string
	services    []string
	seen        map[string]struct{}
}

func (s *Service) buildComposeGroupsInternal(ctx context.Context, sorted []deps.ContainerWithDeps, plansByName map[string]*restartPlan) map[string]composeGroup {
	groups := map[string]composeGroup{}
	if s.config.ProjectUpdater == nil {
		return groups
	}

	for _, candidate := range sorted {
		plan := plansByName[candidate.Name]
		if plan == nil || plan.newRef == "" || plan.inspect == nil || plan.inspect.Config == nil {
			continue
		}
		labels := plan.inspect.Config.Labels
		if s.config.LabelPolicy.IsSelfUpdateTarget(labels) {
			continue
		}
		projectName := utils.ComposeProjectLabel(labels)
		serviceName := utils.ComposeServiceLabel(labels)
		if projectName == "" || serviceName == "" {
			continue
		}
		project, err := s.config.ProjectUpdater.ProjectByComposeName(ctx, projectName)
		if err != nil || project.ID == "" {
			continue
		}
		group := groups[project.ID]
		group.projectName = projectName
		if group.seen == nil {
			group.seen = map[string]struct{}{}
		}
		if _, seen := group.seen[serviceName]; !seen {
			group.services = append(group.services, serviceName)
			group.seen[serviceName] = struct{}{}
		}
		groups[project.ID] = group
	}
	return groups
}

func composeProjectIDInternal(projectName string, groups map[string]composeGroup) string {
	if projectName == "" {
		return ""
	}
	for projectID, group := range groups {
		if group.projectName == projectName {
			return projectID
		}
	}
	return ""
}
