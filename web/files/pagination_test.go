package files

import (
	"encoding/json"
	"net/url"
	"strconv"
	"testing"

	"github.com/cozy/cozy-stack/pkg/couchdb"
	"github.com/cozy/cozy-stack/web/jsonapi"
	"github.com/stretchr/testify/assert"
)

type getDirRes struct {
	Meta struct {
		Count int
	}
	Included []interface{}
}

func getJSON(t *testing.T, url string, out interface{}) error {
	res, err := httpGet(ts.URL + url)
	assert.NoError(t, err)
	defer res.Body.Close()

	assert.Equal(t, 200, res.StatusCode)
	return json.NewDecoder(res.Body).Decode(&out)

}

func TestListDirPaginated(t *testing.T) {

	_, dirdata := createDir(t, "/files/?Type=directory&Name=paginationcontainer")

	dirdata, ok := dirdata["data"].(map[string]interface{})
	assert.True(t, ok)

	parentID, ok := dirdata["id"].(string)
	assert.True(t, ok)

	nb := 15

	body := "foo"
	for i := 0; i < nb; i++ {
		name := "file" + strconv.Itoa(i)
		upload(t, "/files/"+parentID+"?Type=file&Name="+name, "text/plain", body, "rL0Y20zC+Fzt72VPzMSk2A==")
	}

	var opts = &url.Values{}
	opts.Add("page[limit]", "7")
	var result struct {
		Data struct {
			Relationships struct {
				Contents struct {
					Meta struct {
						Count int
					}
					Links *jsonapi.LinksList
					Data  []couchdb.DocReference
				}
			}
		}
		Included []interface{}
	}
	getJSON(t, "/files/"+parentID+"?"+opts.Encode(), &result)

	assert.Len(t, result.Data.Relationships.Contents.Data, 7)
	assert.Len(t, result.Included, 7)
	assert.Equal(t, result.Data.Relationships.Contents.Meta.Count, 15)
	next := result.Data.Relationships.Contents.Links.Next
	assert.NotEmpty(t, next)

	var result2 struct {
		Links *jsonapi.LinksList
		Meta  struct {
			Count int
		}
		Data []interface{}
	}
	getJSON(t, next, &result2)
	assert.Len(t, result2.Data, 7)
	assert.Equal(t, result2.Meta.Count, 15)

	assert.NotEqual(t, result.Data.Relationships.Contents.Data[0].ID,
		result2.Data[0].(map[string]interface{})["id"])

	next = result2.Links.Next
	assert.NotEmpty(t, next)

	var result3 struct {
		Lins *jsonapi.LinksList
		Meta struct {
			Count int
		}
		Data []interface{}
	}
	getJSON(t, next, &result3)
	assert.Len(t, result3.Data, 1)
	assert.Equal(t, result3.Meta.Count, 15)

	assert.NotEqual(t, result.Data.Relationships.Contents.Data[0].ID,
		result3.Data[0].(map[string]interface{})["id"])

	trash(t, "/files/"+parentID)

}

func TestListDirPaginatedSkip(t *testing.T) {

	_, dirdata := createDir(t, "/files/?Type=directory&Name=paginationcontainerskip")

	dirdata, ok := dirdata["data"].(map[string]interface{})
	assert.True(t, ok)

	parentID, ok := dirdata["id"].(string)
	assert.True(t, ok)

	nb := 15

	body := "foo"
	for i := 0; i < nb; i++ {
		name := "file" + strconv.Itoa(i)
		upload(t, "/files/"+parentID+"?Type=file&Name="+name, "text/plain", body, "rL0Y20zC+Fzt72VPzMSk2A==")
	}

	var opts = &url.Values{}
	opts.Add("page[limit]", "7")
	opts.Add("page[skip]", "0")
	var result struct {
		Data struct {
			Relationships struct {
				Contents struct {
					Meta struct {
						Count int
					}
					Links *jsonapi.LinksList
					Data  []couchdb.DocReference
				}
			}
		}
		Included []interface{}
	}
	getJSON(t, "/files/"+parentID+"?"+opts.Encode(), &result)

	assert.Len(t, result.Data.Relationships.Contents.Data, 7)
	assert.Len(t, result.Included, 7)
	assert.Equal(t, result.Data.Relationships.Contents.Meta.Count, 15)
	next := result.Data.Relationships.Contents.Links.Next
	assert.NotEmpty(t, next)
	assert.Contains(t, next, "skip")

	var result2 struct {
		Links *jsonapi.LinksList
		Meta  struct {
			Count int
		}
		Data []interface{}
	}
	getJSON(t, next, &result2)
	assert.Len(t, result2.Data, 7)
	assert.Equal(t, result2.Meta.Count, 15)

	assert.NotEqual(t, result.Data.Relationships.Contents.Data[0].ID,
		result2.Data[0].(map[string]interface{})["id"])

	next = result2.Links.Next
	assert.NotEmpty(t, next)

	var result3 struct {
		Lins *jsonapi.LinksList
		Meta struct {
			Count int
		}
		Data []interface{}
	}
	getJSON(t, next, &result3)
	assert.Len(t, result3.Data, 1)
	assert.Equal(t, result3.Meta.Count, 15)

	assert.NotEqual(t, result.Data.Relationships.Contents.Data[0].ID,
		result3.Data[0].(map[string]interface{})["id"])

	trash(t, "/files/"+parentID)

}
