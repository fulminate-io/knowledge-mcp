// SPDX-License-Identifier: Apache-2.0

package graphclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

func (m *MCPClient) prepareStorageCall(ctx context.Context, params kgtools.CallToolParams) (context.Context, kgtools.CallToolParams, error) {
	if len(params.Arguments) == 0 {
		params.Arguments = json.RawMessage(`{}`)
	}
	var args map[string]any
	decoder := json.NewDecoder(bytes.NewReader(params.Arguments))
	decoder.UseNumber()
	if err := decoder.Decode(&args); err != nil {
		return ctx, params, fmt.Errorf("tool arguments must be a JSON object: %w", err)
	}
	if args == nil {
		return ctx, params, fmt.Errorf("tool arguments must be a JSON object")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return ctx, params, fmt.Errorf("tool arguments must contain exactly one JSON object")
	}
	storage := ""
	if value, ok := args["storage"]; ok {
		var valid bool
		storage, valid = value.(string)
		if !valid {
			return ctx, params, fmt.Errorf("storage must be local or cloud")
		}
	}
	primary, storage, err := storageCallPrimary(args, storage)
	if err != nil {
		return ctx, params, err
	}
	bound, err := m.cfg.BindStorage(ctx, storage)
	if err != nil {
		return ctx, params, err
	}
	if primary != nil {
		bound = WithDestination(bound, primary.Destination)
	}
	d, _ := StorageDestination(bound)
	if d.Storage == "cloud" && d.AccountID == "" {
		return ctx, params, fmt.Errorf("select a cloud account before using cloud storage: run knowledge accounts, then knowledge account use <id|slug>")
	}
	if err := validateStorageCallArguments(args, d, params.Name); err != nil {
		return ctx, params, err
	}
	queries, _ := args["queries"].([]any)
	searchCall := params.Name == "search" || (params.Name == "query" && (args["text"] != "" && args["text"] != nil || len(queries) != 0) && args["id"] == nil && args["ids"] == nil)
	if searchCall {
		bound = WithSearchDestinations(bound, []Destination{d})
		if storage == "" && m.cfg.BindSearch != nil {
			bound, err = m.cfg.BindSearch(bound)
			if err != nil {
				return ctx, params, err
			}
		}
	}
	if _, present := args["storage"]; !present {
		return bound, params, nil
	}
	delete(args, "storage")
	prepared, err := json.Marshal(args)
	if err != nil {
		return ctx, params, err
	}
	params.Arguments = prepared
	return bound, params, nil
}

// A list containing only refs to one destination needs no ambient account to
// select that destination. Mixed and plain lists keep the normal default owner.
func homogeneousStorageReferences(args map[string]any) (*Reference, error) {
	ids, _ := args["ids"].([]any)
	if args["operation"] == "update_batch" {
		items, _ := args["items"].([]any)
		for _, item := range items {
			fields, _ := item.(map[string]any)
			ids = append(ids, fields["id"])
		}
	}
	var candidate *Reference
	for _, value := range ids {
		id, _ := value.(string)
		ref, qualified, err := ParseReference(id)
		if err != nil {
			return nil, err
		}
		if !qualified {
			return nil, nil
		}
		if candidate != nil && candidate.Destination != ref.Destination {
			return nil, nil
		}
		candidate = &ref
	}
	return candidate, nil
}

func validateArgumentReferences(value any, destination Destination, tool string) error {
	switch v := value.(type) {
	case string:
		ref, qualified, err := ParseReference(v)
		if err != nil {
			return err
		}
		if qualified && destination.Storage == "cloud" && ref.Storage == "local" && tool != "query" && tool != "search" && tool != "traverse" {
			return fmt.Errorf("cloud-to-local relationships are not allowed")
		}
		if qualified && destination.Storage == "cloud" && ref.Storage == "cloud" && destination.AccountID != ref.AccountID && tool != "query" && tool != "search" && tool != "traverse" {
			return fmt.Errorf("cross-account relationships are not allowed")
		}
		trimmed := strings.TrimSpace(v)
		if strings.HasPrefix(trimmed, "[") || strings.HasPrefix(trimmed, "{") {
			var decoded any
			if json.Unmarshal([]byte(trimmed), &decoded) == nil {
				return validateArgumentReferences(decoded, destination, tool)
			}
		}
	case []any:
		for _, item := range v {
			if err := validateArgumentReferences(item, destination, tool); err != nil {
				return err
			}
		}
	case map[string]any:
		for _, item := range v {
			if err := validateArgumentReferences(item, destination, tool); err != nil {
				return err
			}
		}
	}
	return nil
}

// Bulk target IDs select owners; fields on each item are relationships owned by
// that item's destination. Validate those fields before any interceptor writes.
func validateStorageCallArguments(args map[string]any, destination Destination, tool string) error {
	if tool != "mutate" && tool != "delete" {
		return validateArgumentReferences(args, destination, tool)
	}
	for key, value := range args {
		if key == "id" || key == "ids" {
			continue
		}
		if key == "items" && args["operation"] == "update_batch" {
			if err := validateStorageUpdateItems(value, destination, tool); err != nil {
				return err
			}
			continue
		}
		if err := validateArgumentReferences(value, destination, tool); err != nil {
			return err
		}
	}
	return nil
}

func storageCallPrimary(args map[string]any, storage string) (*Reference, string, error) {
	var primary *Reference
	for _, key := range []string{"id", "node_id", "start", "from", "thought", "thought_id"} {
		id, _ := args[key].(string)
		ref, qualified, err := ParseReference(id)
		if err != nil {
			return nil, storage, err
		}
		if qualified {
			if storage != "" && storage != ref.Storage {
				return nil, storage, fmt.Errorf("storage conflicts with qualified reference")
			}
			storage = ref.Storage
			primary = &ref
			break
		}
	}
	if primary != nil {
		return primary, storage, nil
	}
	primary, err := homogeneousStorageReferences(args)
	if err != nil {
		return nil, storage, err
	}
	if primary == nil {
		return nil, storage, nil
	}
	if storage != "" && storage != primary.Storage {
		return nil, storage, fmt.Errorf("storage conflicts with qualified references")
	}
	storage = primary.Storage

	return primary, storage, nil
}

func validateStorageUpdateItems(value any, destination Destination, tool string) error {
	items, _ := value.([]any)
	for _, item := range items {
		fields, _ := item.(map[string]any)
		d := destination
		id, _ := fields["id"].(string)
		ref, qualified, err := ParseReference(id)
		if err != nil {
			return err
		}
		if qualified {
			d = ref.Destination
		}
		for field, content := range fields {
			if field != "id" {
				if err := validateArgumentReferences(content, d, tool); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
