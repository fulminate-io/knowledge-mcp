// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/storage/armstorage"
	"github.com/stretchr/testify/assert"
)

func TestParseStorageAccountID(t *testing.T) {
	cases := []struct {
		id       string
		wantRG   string
		wantName string
	}{
		{
			id:       "/subscriptions/sub/resourceGroups/my-rg/providers/Microsoft.Storage/storageAccounts/myacct",
			wantRG:   "my-rg",
			wantName: "myacct",
		},
		{
			id:       "/subscriptions/sub/resourcegroups/my-rg/providers/Microsoft.Storage/storageaccounts/myacct",
			wantRG:   "my-rg",
			wantName: "myacct",
		},
		{
			id:       "",
			wantRG:   "",
			wantName: "",
		},
		{
			id:       "/bogus/path",
			wantRG:   "",
			wantName: "",
		},
	}

	for _, c := range cases {
		rg, name := parseStorageAccountID(c.id)
		assert.Equal(t, c.wantRG, rg, "id=%s rg", c.id)
		assert.Equal(t, c.wantName, name, "id=%s name", c.id)
	}
}

func TestFileShareResourceSpec(t *testing.T) {
	accountID := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Storage/storageAccounts/myacct"
	loc := "westus"
	shareID := accountID + "/fileServices/default/shares/myshare"
	shareName := "myshare"
	quota := int32(5120)

	account := &armstorage.Account{
		ID:       &accountID,
		Location: &loc,
	}
	share := &armstorage.FileShareItem{
		ID:   &shareID,
		Name: &shareName,
		Properties: &armstorage.FileShareProperties{
			ShareQuota: &quota,
		},
	}

	spec := fileShareResourceSpec(account, share, []byte("{}"))
	assert.Equal(t, shareID, spec.ID)
	assert.Equal(t, shareName, spec.Name)
	assert.Equal(t, "Microsoft.Storage/storageAccounts/fileServices/shares", spec.ResourceType)
	assert.Equal(t, loc, spec.Region)
	assert.Equal(t, "5120", spec.Metadata["shareQuotaGiB"])
}
