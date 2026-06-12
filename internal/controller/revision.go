package controller

import "github.com/docker/docker/api/types"

func groupContainersByService(containers []types.Container) map[string][]types.Container {
	services := make(map[string][]types.Container)
	for _, ctr := range containers {
		name := ctr.Labels["com.docker.compose.service"]
		if name == "" {
			continue
		}
		services[name] = append(services[name], ctr)
	}
	return services
}

func filterServiceContainers(containers []types.Container, serviceName string) []types.Container {
	var matched []types.Container
	for _, container := range containers {
		if container.Labels["com.docker.compose.service"] == serviceName {
			matched = append(matched, container)
		}
	}

	return matched
}

func groupByRevision(containers []types.Container) map[string][]types.Container {
	grouped := make(map[string][]types.Container)
	for _, container := range containers {
		revision := containerRevision(container)
		grouped[revision] = append(grouped[revision], container)
	}

	return grouped
}

func containerRevision(container types.Container) string {
	if hash := container.Labels["com.docker.compose.config-hash"]; hash != "" {
		return "config:" + hash
	}

	return "image:" + container.ImageID
}

func splitByRevision(containers []types.Container, revisions map[string][]types.Container) (old, new []types.Container) {
	var newestTime int64
	var newestRevision string
	for _, ctr := range containers {
		if ctr.Created > newestTime {
			newestTime = ctr.Created
			newestRevision = containerRevision(ctr)
		}
	}

	for revision, ctrs := range revisions {
		if revision == newestRevision {
			new = ctrs
		} else {
			old = append(old, ctrs...)
		}
	}

	return old, new
}

// separateByRevision splits containers into the revision group the just-started
// container belongs to ("new") and everything else ("old"). Grouping by revision
// (compose config-hash, falling back to image) means a config-only change with an
// unchanged image still triggers a rollout, matching the CLI release path.
func separateByRevision(containers []types.Container, revisions map[string][]types.Container, startedID string) (oldContainers, newContainers []types.Container) {
	startedRevision := ""
	for _, container := range containers {
		if container.ID == startedID {
			startedRevision = containerRevision(container)
			break
		}
	}

	if startedRevision == "" {
		return nil, nil
	}

	for revision, ctrs := range revisions {
		if revision == startedRevision {
			newContainers = ctrs
			continue
		}

		oldContainers = append(oldContainers, ctrs...)
	}

	return oldContainers, newContainers
}
