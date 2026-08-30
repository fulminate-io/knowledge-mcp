// SPDX-License-Identifier: Apache-2.0

package embed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureVoyageRequest stands up a server that records the decoded request
// body of the first embeddings call and answers with one 1-byte vector per
// input, then builds the arm THROUGH THE REGISTRY against it.
func captureVoyageRequest(t *testing.T, cfg *Config) voyageEmbedRequest {
	t.Helper()
	var got voyageEmbedRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// assert, not require: this runs on the server's goroutine, and
		// require's FailNow is only valid on the test goroutine.
		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&got)) {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		out := struct {
			Data []struct {
				Embedding []int `json:"embedding"`
			} `json:"data"`
		}{}
		for range got.Input {
			out.Data = append(out.Data, struct {
				Embedding []int `json:"embedding"`
			}{Embedding: []int{7}})
		}
		assert.NoError(t, json.NewEncoder(w).Encode(out))
	}))
	t.Cleanup(server.Close)

	cfg.BaseURL = server.URL
	e, err := NewEmbedder(context.Background(), cfg)
	require.NoError(t, err)
	_, err = e.EmbedBinary(context.Background(), "package main")
	require.NoError(t, err)
	return got
}

// TestVoyageArm_ConfigDrivenRequestBody proves the four request fields
// that used to be literals now come from the resolved config: model,
// output_dimension, output_dtype and input_type.
func TestVoyageArm_ConfigDrivenRequestBody(t *testing.T) {
	got := captureVoyageRequest(t, &Config{
		Provider:  ProviderVoyage,
		APIKey:    "k",
		Model:     "voyage-3-large",
		Dimension: 256,
		Dtype:     "ubinary",
		InputRole: InputRoleQuery,
	})

	assert.Equal(t, "voyage-3-large", got.Model, "model must come from config, not a literal")
	assert.Equal(t, 256, got.OutputDim)
	assert.Equal(t, "ubinary", got.OutputType)
	assert.Equal(t, "query", got.InputType, "the query role must post Voyage's query spelling")
	assert.Equal(t, []string{"package main"}, got.Input)
}

// TestVoyageArm_RolesPostDistinctInputTypes captures ONE PAIR of requests
// — a document-role embedder and a query-role embedder built from
// otherwise identical configs — and asserts the posted input_type values
// DIFFER. A single-role assertion cannot catch a hardcoded value; two
// roles that post the same string is exactly the defect being fixed.
func TestVoyageArm_RolesPostDistinctInputTypes(t *testing.T) {
	base := func(role InputRole) *Config {
		return &Config{Provider: ProviderVoyage, APIKey: "k", Dimension: 256, Dtype: "ubinary", InputRole: role}
	}
	doc := captureVoyageRequest(t, base(InputRoleDocument))
	query := captureVoyageRequest(t, base(InputRoleQuery))

	assert.Equal(t, "document", doc.InputType)
	assert.Equal(t, "query", query.InputType)
	assert.NotEqual(t, doc.InputType, query.InputType, "the two roles must post DISTINCT input_type values")
}

// TestVoyageArm_EmptyModelAndBaseURLFallBack proves the arm supplies its
// own defaults when config leaves them empty — the ordinary no-[embedder]
// case, which is what keeps behavior byte-identical for an operator who
// adds no new sections.
func TestVoyageArm_EmptyModelAndBaseURLFallBack(t *testing.T) {
	// Model empty -> DefaultModel, asserted on the wire.
	got := captureVoyageRequest(t, &Config{
		Provider: ProviderVoyage, APIKey: "k", Dimension: 256, Dtype: "ubinary",
	})
	assert.Equal(t, DefaultModel, got.Model, "an empty model must fall back to the arm's default")
	assert.Equal(t, "document", got.InputType, "an empty role must default to document")

	// BaseURL empty -> DefaultBaseURL. Asserted on the constructed arm
	// rather than over the wire: pointing a test at the real endpoint is
	// exactly what this suite must not do.
	e, err := NewEmbedder(context.Background(), &Config{
		Provider: ProviderVoyage, APIKey: "k", Dimension: 256, Dtype: "ubinary",
	})
	require.NoError(t, err)
	arm, ok := e.(*voyageEmbedder)
	require.True(t, ok, "the registered voyage factory must return *voyageEmbedder")
	assert.Equal(t, DefaultBaseURL, arm.BaseURL, "an empty base_url must fall back to the arm's default")
	assert.Equal(t, DefaultModel, arm.Model)
	assert.True(t, arm.Available())
}

// TestVoyageArm_SelfRegisters proves the init() registration landed: the
// provider is present in the registry with no blank import anywhere.
func TestVoyageArm_SelfRegisters(t *testing.T) {
	require.True(t, HasProvider(ProviderVoyage), "the voyage arm must self-register from init()")
}

// TestNewVoyageBinaryEmbedder_SuppliesWidth proves the thin wrapper sets
// the accepted width and dtype EXPLICITLY. Left at zero values the wrapper
// would build a Config that Validate refuses, and the exported constructor
// has no error return to surface that with.
func TestNewVoyageBinaryEmbedder_SuppliesWidth(t *testing.T) {
	e := NewVoyageBinaryEmbedder("k")
	require.NotNil(t, e, "the wrapper must not hand back a nil embedder")
	arm, ok := e.(*voyageEmbedder)
	require.True(t, ok)
	assert.Equal(t, 256, arm.Dimension)
	assert.Equal(t, "ubinary", arm.Dtype)
	// The same values must satisfy Validate — the wrapper and the gate
	// have to agree, which is the whole point of setting them explicitly.
	cfg := &Config{Provider: ProviderVoyage, APIKey: "k", Dimension: arm.Dimension, Dtype: arm.Dtype}
	require.NoError(t, cfg.Validate())
}
