// SPDX-License-Identifier: Apache-2.0

package linear

// Read-side GraphQL queries. Hand-rolled per ticket constraint (no codegen,
// no genqlient). Response shapes are minimal — only the fields the adapter
// actually consumes. Adding a new field means editing the query AND the
// response struct here.

// teamsQuery lists every team the API key can see. Maps to backends.Group
// via Backend.Groups in backend.go.
const teamsQuery = `query Teams {
  teams(first: 250) {
    nodes { id key name }
  }
}`

type teamsResponse struct {
	Teams struct {
		Nodes []struct {
			ID   string `json:"id"`
			Key  string `json:"key"`
			Name string `json:"name"`
		} `json:"nodes"`
	} `json:"teams"`
}

// labelNode is the shared element type for label children inside project /
// issue responses. Named (not anon-struct) so backend.go's joinLabelNames
// helper can take []labelNode with a stable signature.
type labelNode struct {
	Name string `json:"name"`
}

// projectNode is the element type for one project under a team. Named (not
// anon-struct) so backend.go's projectNodeToSnapshot can take it by value.
// `State` is a scalar string here because Project.state in Linear is a
// workspace-level enum (NOT a team-scoped workflow state — that's Issue.state).
//
// Linear distinguishes Description (short tagline, ≤255) from Content (long
// markdown body). The write path maps our Summary→Description and
// Description→Content (see backend_write_project.go); the read path mirrors
// that mapping in projectNodeToSnapshot so round-trips preserve both fields.
type projectNode struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"` // Linear's short tagline, ≤255 chars — maps to our Summary
	Content     string `json:"content"`     // Linear's long markdown body — maps to our Description
	URL         string `json:"url"`
	State       string `json:"state"` // workspace-level enum string
	Priority    int    `json:"priority"`
	Labels      struct {
		Nodes []labelNode `json:"nodes"`
	} `json:"labels"`
	ArchivedAt *string `json:"archivedAt"` // nil = not archived
}

// issueNode is the element type for one issue under a team. Named so
// backend.go's issueNodeToSnapshot can take it by value. `State` is a
// nested object because Issue.state is a team-scoped WorkflowState.
type issueNode struct {
	ID          string `json:"id"`
	Identifier  string `json:"identifier"`
	Title       string `json:"title"`
	Description string `json:"description"`
	URL         string `json:"url"`
	State       struct {
		Name string `json:"name"`
	} `json:"state"`
	Priority int `json:"priority"`
	Labels   struct {
		Nodes []labelNode `json:"nodes"`
	} `json:"labels"`
	Project *struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		URL  string `json:"url"`
	} `json:"project"`
	ArchivedAt *string `json:"archivedAt"`
}

// teamProjectsQuery paginates projects under one team UUID. The variables
// are {teamID: String!, after: String}. Loop until pageInfo.hasNextPage is
// false. Linear's Query.team only accepts id (not key); SyncGroup resolves
// key→id via Groups() before invoking this query.
const teamProjectsQuery = `query TeamProjects($teamID: String!, $after: String) {
  team(id: $teamID) {
    id key name
    projects(first: 100, after: $after, includeArchived: true) {
      pageInfo { hasNextPage endCursor }
      nodes {
        id name description content url
        state
        priority
        labels(first: 50) { nodes { name } }
        archivedAt
      }
    }
  }
}`

type teamProjectsResponse struct {
	Team *struct {
		ID       string `json:"id"`
		Key      string `json:"key"`
		Name     string `json:"name"`
		Projects struct {
			PageInfo struct {
				HasNextPage bool   `json:"hasNextPage"`
				EndCursor   string `json:"endCursor"`
			} `json:"pageInfo"`
			Nodes []projectNode `json:"nodes"`
		} `json:"projects"`
	} `json:"team"`
}

// teamIssuesQuery paginates issues. includeArchived: true so SyncGroup
// covers archived issues per ticket failure-mode list. Same key→id
// resolution as teamProjectsQuery — Linear's Query.team takes id only.
const teamIssuesQuery = `query TeamIssues($teamID: String!, $after: String) {
  team(id: $teamID) {
    issues(first: 100, after: $after, includeArchived: true) {
      pageInfo { hasNextPage endCursor }
      nodes {
        id identifier title description url
        state { name }
        priority
        labels(first: 50) { nodes { name } }
        project { id name url }
        archivedAt
      }
    }
  }
}`

type teamIssuesResponse struct {
	Team *struct {
		Issues struct {
			PageInfo struct {
				HasNextPage bool   `json:"hasNextPage"`
				EndCursor   string `json:"endCursor"`
			} `json:"pageInfo"`
			Nodes []issueNode `json:"nodes"`
		} `json:"issues"`
	} `json:"team"`
}
