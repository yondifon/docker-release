package controller

// serviceKey uniquely identifies a Compose service across projects.
// In per-project mode the project field equals c.project for every key.
// In global mode (c.project == "") the project field comes from container labels.
type serviceKey struct {
	project string
	service string
}

// deployKey returns the string used as the deployments-map key and in log
// messages. Format: "project/service" so two projects with the same service
// name never collide.
func (k serviceKey) deployKey() string {
	if k.project == "" {
		return k.service
	}
	return k.project + "/" + k.service
}
