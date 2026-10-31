package clickup

// Reference is a task's reference in the form ClickUp's GitHub, GitLab and
// Bitbucket integrations recognise in a branch name, commit message or pull
// request: CU-<task id>.
func Reference(externalID string) string { return "CU-" + externalID }

// Reference names the task's reference; see the package-level Reference.
func (p *Provider) Reference(externalID string) string { return Reference(externalID) }
