// SPDX-License-Identifier: Apache-2.0

package gitlab

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	gl "gitlab.com/gitlab-org/api/client-go"
)

const maxRecursionDepth = 10

// projectLister provides shared project discovery with sync.Once semantics.
// All subcollectors that need per-project iteration share a single instance,
// ensuring the group projects API is called at most once per collection run.
type projectLister struct {
	client *gl.Client
	group  string

	once     sync.Once
	projects []*gl.Project
	err      error
}

// list returns all projects in the group (including subgroups, recursively).
// The result is cached after the first call.
func (pl *projectLister) list(ctx context.Context) ([]*gl.Project, error) {
	pl.once.Do(func() {
		pl.projects, pl.err = pl.fetchAll(ctx)
	})
	return pl.projects, pl.err
}

// fetchAll discovers all projects in the group and its subgroups.
func (pl *projectLister) fetchAll(ctx context.Context) ([]*gl.Project, error) {
	var all []*gl.Project

	// Collect top-level group projects.
	projects, err := pl.listGroupProjects(ctx, pl.group)
	if err != nil {
		return nil, fmt.Errorf("list group projects %s: %w", pl.group, err)
	}
	all = append(all, projects...)

	// Recursively walk subgroups.
	subProjects, err := pl.walkSubgroups(ctx, pl.group, 0)
	if err != nil {
		return nil, err
	}
	all = append(all, subProjects...)

	slog.Info("gitlab: project discovery complete", "group", pl.group, "total", len(all))
	return all, nil
}

// walkSubgroups recursively discovers projects in subgroups up to maxRecursionDepth.
func (pl *projectLister) walkSubgroups(ctx context.Context, group string, depth int) ([]*gl.Project, error) {
	if depth >= maxRecursionDepth {
		slog.Warn("gitlab: subgroup recursion depth limit reached", "group", group, "depth", depth)
		return nil, nil
	}

	subgroups, err := pl.listSubgroups(ctx, group)
	if err != nil {
		return nil, fmt.Errorf("list subgroups of %s: %w", group, err)
	}

	var all []*gl.Project
	for _, sg := range subgroups {
		projects, err := pl.listGroupProjects(ctx, sg.FullPath)
		if err != nil {
			slog.Warn("gitlab: failed listing projects in subgroup", "subgroup", sg.FullPath, "error", err)
			continue
		}
		all = append(all, projects...)

		children, err := pl.walkSubgroups(ctx, sg.FullPath, depth+1)
		if err != nil {
			slog.Warn("gitlab: failed walking subgroup children", "subgroup", sg.FullPath, "error", err)
			continue
		}
		all = append(all, children...)
	}
	return all, nil
}

// listGroupProjects paginates through all projects in a single group.
func (pl *projectLister) listGroupProjects(ctx context.Context, group string) ([]*gl.Project, error) {
	var all []*gl.Project
	opts := &gl.ListGroupProjectsOptions{
		ListOptions:      gl.ListOptions{PerPage: 100},
		IncludeSubGroups: new(false),
	}

	for {
		projects, resp, err := pl.client.Groups.ListGroupProjects(group, opts, gl.WithContext(ctx))
		if err != nil {
			return nil, err
		}
		all = append(all, projects...)
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return all, nil
}

// listSubgroups paginates through all subgroups of a group.
func (pl *projectLister) listSubgroups(ctx context.Context, group string) ([]*gl.Group, error) {
	var all []*gl.Group
	opts := &gl.ListSubGroupsOptions{
		ListOptions: gl.ListOptions{PerPage: 100},
	}

	for {
		groups, resp, err := pl.client.Groups.ListSubGroups(group, opts, gl.WithContext(ctx))
		if err != nil {
			return nil, err
		}
		all = append(all, groups...)
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return all, nil
}
