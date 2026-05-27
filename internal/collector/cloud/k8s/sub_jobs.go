// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// jobsSubCollector lists all Jobs and CronJobs across all namespaces.
type jobsSubCollector struct {
	clientset kubernetes.Interface
}

func (s *jobsSubCollector) Name() string { return "jobs" }

func (s *jobsSubCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	var result cloud.SubCollectorResult

	// Jobs.
	jobs, err := s.clientset.BatchV1().Jobs("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return cloud.SubCollectorResult{}, fmt.Errorf("list jobs: %w", err)
	}

	for _, job := range jobs.Items {
		id := resourceID(job.Namespace, "Job", job.Name)

		meta := labelsToMeta(job.Labels)
		meta["namespace"] = job.Namespace
		if job.Spec.Completions != nil {
			meta["completions"] = formatInt32(*job.Spec.Completions)
		}
		if job.Spec.Parallelism != nil {
			meta["parallelism"] = formatInt32(*job.Spec.Parallelism)
		}
		meta["succeeded"] = formatInt32(job.Status.Succeeded)
		meta["failed"] = formatInt32(job.Status.Failed)

		result.Resources = append(result.Resources, cloud.ResourceSpec{
			ID:           id,
			Name:         job.Name,
			ResourceType: "Job",
			Region:       job.Namespace,
			Content:      marshalJSON(job),
			Metadata:     meta,
		})

		// OwnerReference edges (typically to CronJob).
		for _, ref := range job.OwnerReferences {
			ownerID := resourceID(job.Namespace, ref.Kind, ref.Name)
			result.Edges = append(result.Edges, cloud.EdgeSpec{
				SourceID:     id,
				TargetID:     ownerID,
				Relationship: kgtypes.EdgeOwnedBy,
			})
		}

		// Pod template edges (SA, ConfigMap, Secret, PVC).
		result.Edges = append(result.Edges, extractPodTemplateEdges(id, job.Namespace, job.Spec.Template.Spec)...)

		// Cascade targets from container images.
		result.Targets = append(result.Targets, extractImageTargets(job.Spec.Template.Spec.Containers)...)
	}

	// CronJobs.
	cronJobs, err := s.clientset.BatchV1().CronJobs("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return cloud.SubCollectorResult{}, fmt.Errorf("list cronjobs: %w", err)
	}

	for _, cj := range cronJobs.Items {
		id := resourceID(cj.Namespace, "CronJob", cj.Name)

		meta := labelsToMeta(cj.Labels)
		meta["namespace"] = cj.Namespace
		meta["schedule"] = cj.Spec.Schedule
		if cj.Spec.Suspend != nil && *cj.Spec.Suspend {
			meta["suspended"] = "true"
		}

		result.Resources = append(result.Resources, cloud.ResourceSpec{
			ID:           id,
			Name:         cj.Name,
			ResourceType: "CronJob",
			Region:       cj.Namespace,
			Content:      marshalJSON(cj),
			Metadata:     meta,
		})

		// Pod template edges from the job template.
		result.Edges = append(result.Edges, extractPodTemplateEdges(id, cj.Namespace, cj.Spec.JobTemplate.Spec.Template.Spec)...)

		// Cascade targets from container images.
		result.Targets = append(result.Targets, extractImageTargets(cj.Spec.JobTemplate.Spec.Template.Spec.Containers)...)
	}

	return result, nil
}
