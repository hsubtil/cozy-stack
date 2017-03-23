package data

import (
	"net/http"
	"testing"
	"time"

	"github.com/cozy/cozy-stack/pkg/consts"
	"github.com/cozy/cozy-stack/pkg/couchdb"
	"github.com/cozy/cozy-stack/pkg/vfs"
	"github.com/cozy/cozy-stack/web/jsonapi"
	"github.com/stretchr/testify/assert"
)

func TestListReferencesHandler(t *testing.T) {
	// Make doc
	doc := getDocForTest()
	url := ts.URL + "/data/" + doc.DocType() + "/" + doc.ID() + "/relationships/references"

	// Make Files
	makeReferencedTestFile(t, doc, "testtoref2.txt")
	makeReferencedTestFile(t, doc, "testtoref3.txt")
	makeReferencedTestFile(t, doc, "testtoref4.txt")
	makeReferencedTestFile(t, doc, "testtoref5.txt")

	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Add("Authorization", "Bearer "+token)

	var result struct {
		Data []jsonapi.ResourceIdentifier `json:"data"`
	}

	_, res, err := doRequest(req, &result)

	assert.NoError(t, err)
	assert.Equal(t, 200, res.StatusCode)
	assert.Len(t, result.Data, 4)
}

func TestAddReferencesHandler(t *testing.T) {
	// Make doc
	doc := getDocForTest()
	url := ts.URL + "/data/" + doc.DocType() + "/" + doc.ID() + "/relationships/references"

	// Make File
	name := "testtoref.txt"
	dirID := consts.RootDirID
	filedoc, err := vfs.NewFileDoc(name, dirID, -1, nil, "", "", time.Now(), false, nil)
	if !assert.NoError(t, err) {
		return
	}

	f, err := testInstance.VFS().CreateFile(filedoc, nil)
	if !assert.NoError(t, err) {
		return
	}
	if err = f.Close(); !assert.NoError(t, err) {
		return
	}

	// update it
	var in = jsonReader(jsonapi.Relationship{
		Data: []jsonapi.ResourceIdentifier{
			{
				ID:   filedoc.ID(),
				Type: filedoc.DocType(),
			},
		},
	})
	req, _ := http.NewRequest("POST", url, in)
	req.Header.Add("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/vnd.api+json")

	res, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	defer res.Body.Close()
	assert.Equal(t, 204, res.StatusCode)

	fdoc, err := testInstance.VFS().FileByID(filedoc.ID())
	assert.NoError(t, err)
	assert.Len(t, fdoc.ReferencedBy, 1)
}

func TestRemoveReferencesHandler(t *testing.T) {
	// Make doc
	doc := getDocForTest()
	url := ts.URL + "/data/" + doc.DocType() + "/" + doc.ID() + "/relationships/references"

	// Make Files
	f6 := makeReferencedTestFile(t, doc, "testtoref6.txt")
	f7 := makeReferencedTestFile(t, doc, "testtoref7.txt")
	f8 := makeReferencedTestFile(t, doc, "testtoref8.txt")
	f9 := makeReferencedTestFile(t, doc, "testtoref9.txt")

	// update it
	var in = jsonReader(jsonapi.Relationship{
		Data: []jsonapi.ResourceIdentifier{
			{ID: f8, Type: consts.Files},
			{ID: f6, Type: consts.Files},
		},
	})
	req, _ := http.NewRequest("DELETE", url, in)
	req.Header.Add("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/vnd.api+json")

	res, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	defer res.Body.Close()
	assert.Equal(t, 204, res.StatusCode)

	fdoc6, err := testInstance.VFS().FileByID(f6)
	assert.NoError(t, err)
	assert.Len(t, fdoc6.ReferencedBy, 0)
	fdoc8, err := testInstance.VFS().FileByID(f8)
	assert.NoError(t, err)
	assert.Len(t, fdoc8.ReferencedBy, 0)

	fdoc7, err := testInstance.VFS().FileByID(f7)
	assert.NoError(t, err)
	assert.Len(t, fdoc7.ReferencedBy, 1)
	fdoc9, err := testInstance.VFS().FileByID(f9)
	assert.NoError(t, err)
	assert.Len(t, fdoc9.ReferencedBy, 1)
}

func makeReferencedTestFile(t *testing.T, doc couchdb.Doc, name string) string {
	dirID := consts.RootDirID
	filedoc, err := vfs.NewFileDoc(name, dirID, -1, nil, "", "", time.Now(), false, nil)
	if !assert.NoError(t, err) {
		return ""
	}

	filedoc.ReferencedBy = []jsonapi.ResourceIdentifier{
		{
			ID:   doc.ID(),
			Type: doc.DocType(),
		},
	}

	f, err := testInstance.VFS().CreateFile(filedoc, nil)
	if !assert.NoError(t, err) {
		return ""
	}
	if err = f.Close(); !assert.NoError(t, err) {
		return ""
	}
	return filedoc.ID()
}
