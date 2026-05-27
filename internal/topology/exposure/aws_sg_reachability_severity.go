// SPDX-License-Identifier: Apache-2.0

package exposure

// aws_sg_reachability_severity.go holds the severity table used by the
// world-open classifier. Severity is conditional on the attached resource
// type and the exposed port — the same 0.0.0.0/0 ingress rule is routine
// on some attachments (an ALB's 443) and catastrophic on others (an EC2's
// 22). Rules come from the plan's OQ8 resolution.

// severityForAttachment returns the finding severity for a given
// (resource_type, port) tuple when a world-open ingress rule exposes that
// port. Returns "" to suppress the finding entirely.
//
// Rules (from plan Phase 6 spec):
//   - ALB:443     → low  (normal public web)
//   - ALB:80      → low  (normal public web redirect)
//   - ALB:22      → low  (unusual but not critical on a load balancer)
//   - EC2:22      → critical (SSH from world)
//   - EC2:3389    → critical (RDP from world)
//   - EC2:<other> → warning
//   - RDS:3306    → critical (MySQL from world)
//   - RDS:5432    → critical (Postgres from world)
//   - RDS:27017   → critical (MongoDB from world)
//   - RDS:6379    → critical (Redis from world)
//   - EFS:2049    → critical (NFS mount exposed)
//   - Lambda:*    → high (mapped to critical — enum lacks a high tier)
//   - Other:<any> → warning (generic attached resource, world-open is bad)
func severityForAttachment(resourceType string, port int) Severity {
	switch resourceType {
	case "elbv2-load-balancer":
		return severityForALB(port)
	case "ec2-instance":
		return severityForEC2(port)
	case "rds-instance", "rds-cluster":
		return severityForRDS(port)
	case "efs-file-system":
		if port == 2049 {
			return SeverityCritical
		}
		return SeverityWarning
	case "lambda-function":
		// Plan says Lambda exposure → high. The Severity enum does not
		// have a distinct High tier; critical is the closest match and
		// matches how classifyCIDR treats /1..15 public CIDRs.
		return SeverityCritical
	case "elasticache-cluster":
		if port == 6379 || port == 11211 {
			return SeverityCritical
		}
		return SeverityWarning
	case "opensearch-domain":
		if port == 9200 || port == 443 {
			return SeverityCritical
		}
		return SeverityWarning
	default:
		return SeverityWarning
	}
}

// severityForALB applies ALB-specific severity rules. Public web ports
// (80, 443) are low severity — that's the whole point of a load balancer.
// Unusual ports are only warnings, not criticals, because the ALB isn't
// running the application logic itself.
func severityForALB(port int) Severity {
	switch port {
	case 80, 443, 8080, 8443:
		return SeverityInfo
	case 22, 3389:
		// Unusual on an ALB but still not terribly dangerous since the
		// ALB doesn't run SSH/RDP. Low severity.
		return SeverityInfo
	default:
		return SeverityNotice
	}
}

// severityForEC2 applies EC2-specific severity rules. SSH (22) and RDP
// (3389) from the world are critical; DB ports on an EC2 suggest a
// self-hosted database which is also critical; HTTP/HTTPS are warnings
// (the instance may legitimately serve web traffic but should ideally
// go through an ALB).
func severityForEC2(port int) Severity {
	switch port {
	case 22, 3389:
		return SeverityCritical
	case 3306, 5432, 27017, 6379, 9200, 11211, 2049:
		return SeverityCritical
	case 80, 443:
		return SeverityWarning
	default:
		return SeverityWarning
	}
}

// severityForRDS applies RDS-specific severity rules. Any world-open
// database port is critical — RDS should never be reachable from the
// internet.
func severityForRDS(port int) Severity {
	switch port {
	case 3306, 5432, 1433, 1521, 27017, 6379:
		return SeverityCritical
	default:
		return SeverityCritical
	}
}
