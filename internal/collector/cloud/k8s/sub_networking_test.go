// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestServicesSubCollector_LoadBalancer(t *testing.T) {
	cs := fake.NewSimpleClientset(&corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "web-lb",
			Namespace: "default",
			Annotations: map[string]string{
				"service.beta.kubernetes.io/aws-load-balancer-type": "nlb",
			},
		},
		Spec: corev1.ServiceSpec{
			Type:      corev1.ServiceTypeLoadBalancer,
			ClusterIP: "10.0.0.1",
			Selector:  map[string]string{"app": "web"},
			Ports: []corev1.ServicePort{
				{Protocol: corev1.ProtocolTCP, Port: 80},
				{Protocol: corev1.ProtocolTCP, Port: 443},
			},
		},
	})

	sub := &servicesSubCollector{clientset: cs}
	result, err := sub.Collect(context.Background())
	require.NoError(t, err)
	require.Len(t, result.Resources, 1)

	res := result.Resources[0]
	assert.Equal(t, "default/Service/web-lb", res.ID)
	assert.Equal(t, "LoadBalancer", res.Metadata["type"])
	assert.Equal(t, "10.0.0.1", res.Metadata["cluster_ip"])
	assert.Equal(t, "TCP/80,TCP/443", res.Metadata["ports"])
	assert.Contains(t, res.Metadata["selector"], "web")

	// AWS cascade target from LB annotation.
	require.Len(t, result.Targets, 1)
	assert.Equal(t, "aws", result.Targets[0].Collector)
}

func TestIngressesSubCollector(t *testing.T) {
	ingressClass := "nginx"
	cs := fake.NewSimpleClientset(&networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "web-ingress",
			Namespace: "default",
			Annotations: map[string]string{
				"alb.ingress.kubernetes.io/scheme": "internet-facing",
			},
		},
		Spec: networkingv1.IngressSpec{
			IngressClassName: &ingressClass,
			DefaultBackend: &networkingv1.IngressBackend{
				Service: &networkingv1.IngressServiceBackend{
					Name: "default-svc",
					Port: networkingv1.ServiceBackendPort{Number: 80},
				},
			},
			Rules: []networkingv1.IngressRule{
				{
					Host: "example.com",
					IngressRuleValue: networkingv1.IngressRuleValue{
						HTTP: &networkingv1.HTTPIngressRuleValue{
							Paths: []networkingv1.HTTPIngressPath{
								{
									Path: "/api",
									Backend: networkingv1.IngressBackend{
										Service: &networkingv1.IngressServiceBackend{
											Name: "api-svc",
											Port: networkingv1.ServiceBackendPort{Number: 8080},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	})

	sub := &ingressesSubCollector{clientset: cs}
	result, err := sub.Collect(context.Background())
	require.NoError(t, err)
	require.Len(t, result.Resources, 1)

	res := result.Resources[0]
	assert.Equal(t, "default/Ingress/web-ingress", res.ID)
	assert.Equal(t, "example.com", res.Metadata["hosts"])
	assert.Equal(t, "nginx", res.Metadata["ingress_class"])

	// ROUTES_TO edges.
	var routeTargets []string
	for _, e := range result.Edges {
		if e.Relationship == kgtypes.EdgeRoutesTo {
			routeTargets = append(routeTargets, e.TargetID)
		}
	}
	assert.Contains(t, routeTargets, "default/Service/default-svc")
	assert.Contains(t, routeTargets, "default/Service/api-svc")

	// ALB annotation → AWS cascade.
	require.Len(t, result.Targets, 1)
	assert.Equal(t, "aws", result.Targets[0].Collector)
}

func TestNetworkPoliciesSubCollector(t *testing.T) {
	cs := fake.NewSimpleClientset(&networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "deny-all",
			Namespace: "secure",
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "backend"},
			},
			PolicyTypes: []networkingv1.PolicyType{
				networkingv1.PolicyTypeIngress,
				networkingv1.PolicyTypeEgress,
			},
			Ingress: []networkingv1.NetworkPolicyIngressRule{
				{
					Ports: []networkingv1.NetworkPolicyPort{
						{Port: portPtr(8080)},
					},
				},
			},
		},
	})

	sub := &networkPoliciesSubCollector{clientset: cs}
	result, err := sub.Collect(context.Background())
	require.NoError(t, err)
	require.Len(t, result.Resources, 1)

	res := result.Resources[0]
	assert.Equal(t, "secure/NetworkPolicy/deny-all", res.ID)
	assert.Equal(t, "Ingress,Egress", res.Metadata["policy_types"])
	assert.Contains(t, res.Metadata["pod_selector"], "backend")
}

func portPtr(port int) *intstr.IntOrString {
	p := intstr.FromInt32(int32(port))
	return &p
}
