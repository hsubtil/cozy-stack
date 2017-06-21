package sharings

import (
	"testing"

	"github.com/cozy/cozy-stack/pkg/consts"
	"github.com/cozy/cozy-stack/pkg/couchdb"
	"github.com/stretchr/testify/assert"
)

func TestUpdateApplicationDestinationDirID(t *testing.T) {
	// Test: update a destination directory.
	slug := "randomslug"
	dirID := "randomdirid"
	doctype := "io.cozy.foos"

	err := UpdateApplicationDestinationDirID(testInstance, slug, doctype, dirID)
	assert.NoError(t, err)

	s := &SharingSettings{}
	err = couchdb.GetDoc(testInstance, consts.Settings,
		consts.SharingSettingsID, s)
	assert.NoError(t, err)
	assert.Equal(t, dirID, s.AppDestination[slug][doctype])

	// Test: update the same destination directory and see if the change was
	// persisted.
	err = UpdateApplicationDestinationDirID(testInstance, slug, doctype,
		"otherdirid")
	assert.NoError(t, err)

	sbis := &SharingSettings{}
	err = couchdb.GetDoc(testInstance, consts.Settings,
		consts.SharingSettingsID, sbis)
	assert.NoError(t, err)
	assert.Equal(t, "otherdirid", sbis.AppDestination[slug][doctype])
}

func TestRetrieveApplicationDestinationDirID(t *testing.T) {
	// Test retrive destination dirID when sharing settings does not exist.
	s := SharingSettings{}
	err := couchdb.GetDoc(testInstance, consts.Settings,
		consts.SharingSettingsID, &s)
	if err == nil {
		err = couchdb.DeleteDoc(testInstance, &s)
		assert.NoError(t, err)
	}

	dirID, err := RetrieveApplicationDestinationDirID(testInstance,
		"randomslug", "io.cozy.files")
	assert.NoError(t, err)

	s = SharingSettings{}
	err = couchdb.GetDoc(testInstance, consts.Settings,
		consts.SharingSettingsID, &s)
	assert.NoError(t, err)
	assert.Equal(t, dirID, s.SharedWithMeDirID)

	// Test: set a destination directory and fetch it afterwards.
	dirDoc := createDir(t, testInstance.VFS(), "retrievetest",
		[]couchdb.DocReference{})
	slug := "randomretrieveslug"
	doctype := "io.cozy.foos.bars"

	err = UpdateApplicationDestinationDirID(testInstance, slug, doctype,
		dirDoc.ID())
	assert.NoError(t, err)

	retrievedDirID, err := RetrieveApplicationDestinationDirID(testInstance,
		slug, doctype)
	assert.NoError(t, err)
	assert.Equal(t, dirDoc.ID(), retrievedDirID)

	// Test: set a destination directory while the directory doesn't exist and
	// check that we receive the shared with me dirid.
	err = UpdateApplicationDestinationDirID(testInstance, slug, consts.Files,
		"randomdirid")
	assert.NoError(t, err)
	s = SharingSettings{}
	err = couchdb.GetDoc(testInstance, consts.Settings,
		consts.SharingSettingsID, &s)
	assert.NoError(t, err)

	retrievedDirID, err = RetrieveApplicationDestinationDirID(testInstance,
		slug, consts.Files)
	assert.NoError(t, err)
	assert.Equal(t, s.SharedWithMeDirID, retrievedDirID)

	// Test: fetch a destination directory for a doctype for which we didn't set
	// any and check that we receive the shared with me dirid.
	defaultDirID, err := RetrieveApplicationDestinationDirID(testInstance, slug,
		"io.cozy.bazs")
	assert.NoError(t, err)
	assert.Equal(t, s.SharedWithMeDirID, defaultDirID)
}
