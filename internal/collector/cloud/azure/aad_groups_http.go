// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"context"
)

// graphListResponse is the generic envelope for paginated Graph API responses.
type graphListResponse[T any] struct {
	Value    []T    `json:"value"`
	NextLink string `json:"@odata.nextLink"`
}

// fetchGroupsPage fetches a single page of groups from the given URL.
func (c *aadGroupsCollector) fetchGroupsPage(
	ctx context.Context, token, url string,
) ([]graphGroup, string, error) {
	body, err := c.graphGET(ctx, token, url)
	if err != nil {
		return nil, "", err
	}
	var resp graphListResponse[graphGroup]
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, "", fmt.Errorf("azure-aad-groups: decode groups: %w", err)
	}
	return resp.Value, resp.NextLink, nil
}

// fetchMembersPage fetches a single page of group members from the given URL.
func (c *aadGroupsCollector) fetchMembersPage(
	ctx context.Context, token, url string,
) ([]graphMember, string, error) {
	body, err := c.graphGET(ctx, token, url)
	if err != nil {
		return nil, "", err
	}
	var resp graphListResponse[graphMember]
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, "", fmt.Errorf("azure-aad-groups: decode members: %w", err)
	}
	return resp.Value, resp.NextLink, nil
}

// graphGET performs an authenticated GET request to the Microsoft Graph API.
func (c *aadGroupsCollector) graphGET(ctx context.Context, token, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("azure-aad-groups: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	client := c.httpClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("azure-aad-groups: http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized {
		return nil, &graphForbiddenError{statusCode: resp.StatusCode}
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("azure-aad-groups: unexpected status %d: %s",
			resp.StatusCode, truncateBody(body))
	}

	return io.ReadAll(resp.Body)
}

// graphForbiddenError is returned when the Graph API returns 401/403.
type graphForbiddenError struct {
	statusCode int
}

func (e *graphForbiddenError) Error() string {
	return fmt.Sprintf("azure-aad-groups: forbidden (HTTP %d)", e.statusCode)
}

// isForbidden reports whether err is a 401/403 response from the Graph API.
func isForbidden(err error) bool {
	if err == nil {
		return false
	}
	_, ok := err.(*graphForbiddenError)
	return ok
}

// truncateBody returns at most 200 bytes of response body for error messages.
func truncateBody(body []byte) string {
	s := strings.TrimSpace(string(body))
	if len(s) > 200 {
		return s[:200] + "..."
	}
	return s
}
