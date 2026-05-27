// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestStepFunctionsCollector_Name(t *testing.T) {
	c := &stepFunctionsCollector{}
	assert.Equal(t, "stepfunctions", c.Name())
}

func TestExtractASLTargets(t *testing.T) {
	tests := []struct {
		name       string
		definition string
		want       []string
	}{
		{
			name: "simple lambda task",
			definition: `{
				"StartAt": "InvokeLambda",
				"States": {
					"InvokeLambda": {
						"Type": "Task",
						"Resource": "arn:aws:lambda:us-east-1:123456789012:function:my-handler",
						"End": true
					}
				}
			}`,
			want: []string{
				"arn:aws:lambda:us-east-1:123456789012:function:my-handler",
			},
		},
		{
			name: "multiple targets lambda sns ecs",
			definition: `{
				"StartAt": "Step1",
				"States": {
					"Step1": {
						"Type": "Task",
						"Resource": "arn:aws:lambda:us-east-1:123456789012:function:step1",
						"Next": "Step2"
					},
					"Step2": {
						"Type": "Task",
						"Resource": "arn:aws:states:::sqs:sendMessage",
						"Parameters": {
							"QueueUrl": "https://sqs.us-east-1.amazonaws.com/123456789012/my-queue",
							"MessageBody": "hello"
						},
						"Next": "Step3"
					},
					"Step3": {
						"Type": "Task",
						"Parameters": {
							"TopicArn": "arn:aws:sns:us-east-1:123456789012:my-topic",
							"Message": "done"
						},
						"Next": "Step4"
					},
					"Step4": {
						"Type": "Task",
						"Resource": "arn:aws:ecs:us-east-1:123456789012:task-definition/my-task:1",
						"End": true
					}
				}
			}`,
			// Regex only matches ARNs with region:account — states::: integration
			// ARNs (e.g. arn:aws:states:::sqs:sendMessage) are not captured.
			// The SNS topic ARN in Parameters IS captured because it has region:account.
			want: []string{
				"arn:aws:lambda:us-east-1:123456789012:function:step1",
				"arn:aws:sns:us-east-1:123456789012:my-topic",
				"arn:aws:ecs:us-east-1:123456789012:task-definition/my-task:1",
			},
		},
		{
			name:       "empty definition",
			definition: "",
			want:       nil,
		},
		{
			name:       "malformed json",
			definition: `{not valid json at all`,
			want:       nil,
		},
		{
			name: "nested parallel states with resources",
			definition: `{
				"StartAt": "Parallel",
				"States": {
					"Parallel": {
						"Type": "Parallel",
						"Branches": [
							{
								"StartAt": "Branch1",
								"States": {
									"Branch1": {
										"Type": "Task",
										"Resource": "arn:aws:lambda:us-east-1:123456789012:function:branch1-fn",
										"End": true
									}
								}
							},
							{
								"StartAt": "Branch2",
								"States": {
									"Branch2": {
										"Type": "Task",
										"Resource": "arn:aws:lambda:us-east-1:123456789012:function:branch2-fn",
										"End": true
									}
								}
							}
						],
						"End": true
					}
				}
			}`,
			want: []string{
				"arn:aws:lambda:us-east-1:123456789012:function:branch1-fn",
				"arn:aws:lambda:us-east-1:123456789012:function:branch2-fn",
			},
		},
		{
			name: "deduplicate same target referenced twice",
			definition: `{
				"StartAt": "A",
				"States": {
					"A": {
						"Type": "Task",
						"Resource": "arn:aws:lambda:us-east-1:123456789012:function:shared",
						"Next": "B"
					},
					"B": {
						"Type": "Task",
						"Resource": "arn:aws:lambda:us-east-1:123456789012:function:shared",
						"End": true
					}
				}
			}`,
			want: []string{
				"arn:aws:lambda:us-east-1:123456789012:function:shared",
			},
		},
		{
			name: "no extractable resources pass state only",
			definition: `{
				"StartAt": "PassState",
				"States": {
					"PassState": {
						"Type": "Pass",
						"Result": {"key": "value"},
						"End": true
					}
				}
			}`,
			want: nil,
		},
		{
			name: "dynamodb table ARN extraction",
			definition: `{
				"StartAt": "ReadTable",
				"States": {
					"ReadTable": {
						"Type": "Task",
						"Resource": "arn:aws:states:::dynamodb:getItem",
						"Parameters": {
							"TableName": "arn:aws:dynamodb:us-east-1:123456789012:table/orders"
						},
						"End": true
					}
				}
			}`,
			want: []string{
				"arn:aws:dynamodb:us-east-1:123456789012:table/orders",
			},
		},
		{
			name: "s3 access point ARN extraction",
			definition: `{
				"StartAt": "PutObject",
				"States": {
					"PutObject": {
						"Type": "Task",
						"Resource": "arn:aws:states:::aws-sdk:S3:putObject",
						"Parameters": {
							"Bucket": "arn:aws:s3:us-east-1:123456789012:accesspoint/my-ap"
						},
						"End": true
					}
				}
			}`,
			want: []string{
				"arn:aws:s3:us-east-1:123456789012:accesspoint/my-ap",
			},
		},
		{
			name: "s3 bucket ARN extraction (no region/account)",
			definition: `{
				"StartAt": "PutObject",
				"States": {
					"PutObject": {
						"Type": "Task",
						"Resource": "arn:aws:states:::aws-sdk:S3:putObject",
						"Parameters": {
							"Bucket": "arn:aws:s3:::my-data-bucket"
						},
						"End": true
					}
				}
			}`,
			want: []string{
				"arn:aws:s3:::my-data-bucket",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractASLTargets(tt.definition)
			assert.Equal(t, tt.want, got)
		})
	}
}
