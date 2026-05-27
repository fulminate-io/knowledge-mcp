// SPDX-License-Identifier: Apache-2.0

// Package cloudwatch implements a logwire.Provider for AWS CloudWatch Logs.
//
// It self-registers via init() so importing this package is enough to
// make "cloudwatch" available through logwire.New("cloudwatch").
package cloudwatch

import (
	"context"
	"fmt"
	"sync"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"

	logwire "github.com/fulminate-io/knowledge-mcp/internal/logwire"
)

func init() {
	logwire.Register("cloudwatch", func() logwire.Provider { return &cloudwatchProvider{} })
}

// cloudwatchProvider implements logwire.Provider for AWS CloudWatch Logs.
// One instance per region/account; use Configure to set credentials.
type cloudwatchProvider struct {
	region    string
	profile   string
	accessKey string
	secretKey string
	logGroup  string

	mu     sync.Mutex
	client *cloudwatchlogs.Client
}

// Configure applies provider-specific settings from the config map.
// Supported keys: region (required), profile, access_key_id,
// secret_access_key, log_group.
func (p *cloudwatchProvider) Configure(cfg map[string]string) error {
	p.region = cfg["region"]
	if p.region == "" {
		return fmt.Errorf("cloudwatch: region is required")
	}
	p.profile = cfg["profile"]
	p.accessKey = cfg["access_key_id"]
	p.secretKey = cfg["secret_access_key"]
	p.logGroup = cfg["log_group"]

	// Reset the lazy client so the next call rebuilds it.
	p.mu.Lock()
	p.client = nil
	p.mu.Unlock()
	return nil
}

// ensureClient lazily initializes the AWS CloudWatch Logs client.
func (p *cloudwatchProvider) ensureClient(ctx context.Context) (*cloudwatchlogs.Client, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.client != nil {
		return p.client, nil
	}

	var opts []func(*config.LoadOptions) error
	opts = append(opts, config.WithRegion(p.region))
	if p.profile != "" {
		opts = append(opts, config.WithSharedConfigProfile(p.profile))
	}
	if p.accessKey != "" && p.secretKey != "" {
		opts = append(opts, config.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(p.accessKey, p.secretKey, ""),
		))
	}

	cfg, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("cloudwatch: load AWS config: %w", err)
	}
	p.client = cloudwatchlogs.NewFromConfig(cfg)
	return p.client, nil
}
