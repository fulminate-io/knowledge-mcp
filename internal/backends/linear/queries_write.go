// SPDX-License-Identifier: Apache-2.0

package linear

// Write-side GraphQL operations: mutation strings, auxiliary lookup
// queries, and matching response shapes. Hand-rolled per ticket constraint
// (no codegen). Adding a new mutation field means editing the const string
// AND the response struct here. Names verified against Linear's public
// schema explorer (https://studio.apollographql.com/public/Linear-API)
// at plan time; mutations match Linear's documented surface
// (projectCreate / projectUpdate / projectArchive / issueCreate /
// issueUpdate / issueArchive / issueLabelCreate).

// teamWithStatesLabels is the shared inner-team type returned by both
// teamByKey and teamByID — same shape, same fields, just keyed
// differently. backend_write.go's resolveTeamByKey / resolveTeamByID
// helpers normalize this into a *resolvedTeam (name → UUID maps).
type teamWithStatesLabels struct {
	ID     string `json:"id"`
	Key    string `json:"key"`
	States struct {
		Nodes []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"nodes"`
	} `json:"states"`
	Labels struct {
		Nodes []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"nodes"`
	} `json:"labels"`
}

// teamByKeyQuery resolves a group key (human-readable, e.g. "ABC") to the
// team's UUID + workflow states + labels in one shot. Used by
// CreateProject / CreateTicket where the caller supplies the group key
// explicitly. Linear's Query.team only takes id; we use the filtered
// teams() list with key={eq:...} to look up by key. Variable: {key: String!}.
const teamByKeyQuery = `query TeamByKey($key: String!) {
  teams(filter: {key: {eq: $key}}, first: 1) {
    nodes {
      id key
      states(first: 100) { nodes { id name } }
      labels(first: 250) { nodes { id name } }
    }
  }
}`

type teamByKeyResponse struct {
	Teams struct {
		Nodes []teamWithStatesLabels `json:"nodes"`
	} `json:"teams"`
}

// teamByIDQuery resolves a team UUID to its workflow states + labels.
// Used by UpdateTicket (after resolving teamID via issueByID) and
// UpdateProject (after resolving teamID via projectByID). Variable:
// {id: String!}.
const teamByIDQuery = `query TeamByID($id: String!) {
  team(id: $id) {
    id key
    states(first: 100) { nodes { id name } }
    labels(first: 250) { nodes { id name } }
  }
}`

type teamByIDResponse struct {
	Team *teamWithStatesLabels `json:"team"`
}

// issueByIDQuery resolves an issue UUID to its team UUID + key. Used by
// UpdateTicket to derive the team for state/label resolution. Issue.team
// is a singular non-null Team in Linear, so the result is unambiguous.
// Variable: {id: String!}.
const issueByIDQuery = `query IssueByID($id: String!) {
  issue(id: $id) {
    id
    team { id key }
  }
}`

type issueByIDResponse struct {
	Issue *struct {
		ID   string `json:"id"`
		Team struct {
			ID  string `json:"id"`
			Key string `json:"key"`
		} `json:"team"`
	} `json:"issue"`
}

// projectByIDQuery resolves a project UUID to its team list. Linear's
// Project.teams is a TeamConnection — projects can span multiple teams.
// UpdateProject's label-resolution path picks the first team Linear
// returns (documented caveat in UpdateProject's doc comment). Variable:
// {id: String!}.
const projectByIDQuery = `query ProjectByID($id: String!) {
  project(id: $id) {
    id
    teams(first: 50) { nodes { id key } }
  }
}`

type projectByIDResponse struct {
	Project *struct {
		ID    string `json:"id"`
		Teams struct {
			Nodes []struct {
				ID  string `json:"id"`
				Key string `json:"key"`
			} `json:"nodes"`
		} `json:"teams"`
	} `json:"project"`
}

// projectCreateMutation creates a project. Variable: {input:
// ProjectCreateInput!}. teamIds is required; state is the workspace-level
// enum string (verbatim — NOT a UUID).
const projectCreateMutation = `mutation ProjectCreate($input: ProjectCreateInput!) {
  projectCreate(input: $input) {
    project { id name url state }
  }
}`

type projectCreateResponse struct {
	ProjectCreate struct {
		Project struct {
			ID    string `json:"id"`
			Name  string `json:"name"`
			URL   string `json:"url"`
			State string `json:"state"`
		} `json:"project"`
	} `json:"projectCreate"`
}

// projectUpdateMutation updates a project. Variables: {id: String!, input:
// ProjectUpdateInput!}. state is again the workspace-level enum string
// passed verbatim — UpdateProject does NOT do team-scoped resolution for
// project status (project state has no team scope in Linear).
const projectUpdateMutation = `mutation ProjectUpdate($id: String!, $input: ProjectUpdateInput!) {
  projectUpdate(id: $id, input: $input) {
    project { id state }
  }
}`

type projectUpdateResponse struct {
	ProjectUpdate struct {
		Project struct {
			ID    string `json:"id"`
			State string `json:"state"`
		} `json:"project"`
	} `json:"projectUpdate"`
}

// projectArchiveMutation archives a project. Variable: {id: String!}.
// Per locked answer to the re-archive question: trust Linear's idempotent
// behavior; T1 does not special-case already-archived items.
const projectArchiveMutation = `mutation ProjectArchive($id: String!) {
  projectArchive(id: $id) {
    success
  }
}`

type projectArchiveResponse struct {
	ProjectArchive struct {
		Success bool `json:"success"`
	} `json:"projectArchive"`
}

// issueCreateMutation creates an issue. Variable: {input: IssueCreateInput!}.
// teamId is required; stateId is a team-scoped WorkflowState UUID.
const issueCreateMutation = `mutation IssueCreate($input: IssueCreateInput!) {
  issueCreate(input: $input) {
    issue { id identifier title url state { name } }
  }
}`

type issueCreateResponse struct {
	IssueCreate struct {
		Issue struct {
			ID         string `json:"id"`
			Identifier string `json:"identifier"`
			Title      string `json:"title"`
			URL        string `json:"url"`
			State      struct {
				Name string `json:"name"`
			} `json:"state"`
		} `json:"issue"`
	} `json:"issueCreate"`
}

// issueUpdateMutation updates an issue. Variables: {id: String!, input:
// IssueUpdateInput!}. stateId is a team-scoped WorkflowState UUID — the
// caller resolves the name → UUID via the team's workflow before calling.
const issueUpdateMutation = `mutation IssueUpdate($id: String!, $input: IssueUpdateInput!) {
  issueUpdate(id: $id, input: $input) {
    issue { id state { name } }
  }
}`

type issueUpdateResponse struct {
	IssueUpdate struct {
		Issue struct {
			ID    string `json:"id"`
			State struct {
				Name string `json:"name"`
			} `json:"state"`
		} `json:"issue"`
	} `json:"issueUpdate"`
}

// issueArchiveMutation archives an issue. Variable: {id: String!}.
const issueArchiveMutation = `mutation IssueArchive($id: String!) {
  issueArchive(id: $id) {
    success
  }
}`

type issueArchiveResponse struct {
	IssueArchive struct {
		Success bool `json:"success"`
	} `json:"issueArchive"`
}

// issueLabelCreateMutation creates a single team-scoped issue label.
// Variable: {input: IssueLabelCreateInput!} — required input fields are
// `name` and `teamId`. Used by ensureLabels to create labels missing
// from the team's label set on the fly. Per the locked answer to the
// label-create question: HARD-ERROR on failure (T1 is an adapter, not a
// concurrency layer; we do not retry, swallow duplicate-name races, or
// fall back).
const issueLabelCreateMutation = `mutation IssueLabelCreate($input: IssueLabelCreateInput!) {
  issueLabelCreate(input: $input) {
    issueLabel { id name }
  }
}`

type issueLabelCreateResponse struct {
	IssueLabelCreate struct {
		IssueLabel struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"issueLabel"`
	} `json:"issueLabelCreate"`
}
