package files

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cozy/cozy-stack/model/instance"
	"github.com/cozy/cozy-stack/model/permission"
	"github.com/cozy/cozy-stack/model/vfs"
	"github.com/cozy/cozy-stack/pkg/config/config"
	"github.com/cozy/cozy-stack/pkg/consts"
	"github.com/cozy/cozy-stack/pkg/couchdb"
	"github.com/cozy/cozy-stack/pkg/limits"
	"github.com/cozy/cozy-stack/tests/testutils"
	"github.com/cozy/cozy-stack/web/errors"
	"github.com/cozy/cozy-stack/web/middlewares"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"

	_ "github.com/cozy/cozy-stack/web/statik"
	_ "github.com/cozy/cozy-stack/worker/thumbnail"
)

var ts *httptest.Server
var testInstance *instance.Instance
var setup *testutils.TestSetup
var token string
var clientID string
var imgID string
var fileID string
var publicToken string

func readFile(fs vfs.VFS, name string) ([]byte, error) {
	doc, err := fs.FileByPath(name)
	if err != nil {
		return nil, err
	}
	f, err := fs.OpenFile(doc)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return ioutil.ReadAll(f)
}

func extractJSONRes(res *http.Response, mp *map[string]interface{}) error {
	if res.StatusCode >= 300 {
		return nil
	}
	return json.NewDecoder(res.Body).Decode(mp)
}

func createDir(t *testing.T, path string) (res *http.Response, v map[string]interface{}) {
	req, err := http.NewRequest("POST", ts.URL+path, strings.NewReader(""))
	if !assert.NoError(t, err) {
		return
	}
	req.Header.Add("Content-Type", "text/plain")
	req.Header.Add(echo.HeaderAuthorization, "Bearer "+token)
	res, err = http.DefaultClient.Do(req)
	if !assert.NoError(t, err) {
		return
	}
	defer res.Body.Close()

	err = extractJSONRes(res, &v)
	assert.NoError(t, err)

	return
}

func doUploadOrMod(t *testing.T, req *http.Request, contentType, hash string) (res *http.Response, v map[string]interface{}) {
	var err error

	if contentType != "" {
		req.Header.Add("Content-Type", contentType)
	}

	if hash != "" {
		req.Header.Add("Content-MD5", hash)
	}

	res, err = http.DefaultClient.Do(req)
	if !assert.NoError(t, err) {
		return
	}

	defer res.Body.Close()

	err = extractJSONRes(res, &v)
	assert.NoError(t, err)

	return
}

func upload(t *testing.T, path, contentType, body, hash string) (res *http.Response, v map[string]interface{}) {
	buf := strings.NewReader(body)
	req, err := http.NewRequest("POST", ts.URL+path, buf)
	if !assert.NoError(t, err) {
		return
	}
	req.Header.Add(echo.HeaderAuthorization, "Bearer "+token)
	if strings.Contains(path, "Size=") {
		req.ContentLength = -1
	}
	return doUploadOrMod(t, req, contentType, hash)
}

func uploadMod(t *testing.T, path, contentType, body, hash string) (res *http.Response, v map[string]interface{}) {
	buf := strings.NewReader(body)
	req, err := http.NewRequest("PUT", ts.URL+path, buf)
	req.Header.Add(echo.HeaderAuthorization, "Bearer "+token)
	if !assert.NoError(t, err) {
		return
	}
	return doUploadOrMod(t, req, contentType, hash)
}

func trash(t *testing.T, path string) (res *http.Response, v map[string]interface{}) {
	req, err := http.NewRequest(http.MethodDelete, ts.URL+path, nil)
	req.Header.Add(echo.HeaderAuthorization, "Bearer "+token)
	if !assert.NoError(t, err) {
		return
	}

	res, err = http.DefaultClient.Do(req)
	if !assert.NoError(t, err) {
		return
	}

	err = extractJSONRes(res, &v)
	assert.NoError(t, err)

	return
}

func restore(t *testing.T, path string) (res *http.Response, v map[string]interface{}) {
	req, err := http.NewRequest(http.MethodPost, ts.URL+path, nil)
	req.Header.Add(echo.HeaderAuthorization, "Bearer "+token)
	if !assert.NoError(t, err) {
		return
	}

	res, err = http.DefaultClient.Do(req)
	if !assert.NoError(t, err) {
		return
	}

	err = extractJSONRes(res, &v)
	assert.NoError(t, err)

	return
}

func extractDirData(t *testing.T, data map[string]interface{}) (string, map[string]interface{}) {
	var ok bool

	data, ok = data["data"].(map[string]interface{})
	if !assert.True(t, ok) {
		return "", nil
	}

	id, ok := data["id"].(string)
	if !assert.True(t, ok) {
		return "", nil
	}

	return id, data
}

type jsonData struct {
	Type  string                 `json:"type"`
	ID    string                 `json:"id"`
	Attrs map[string]interface{} `json:"attributes,omitempty"`
	Rels  map[string]interface{} `json:"relationships,omitempty"`
}

func patchFile(t *testing.T, path, docType, id string, attrs map[string]interface{}, parent *jsonData) (res *http.Response, v map[string]interface{}) {
	bodyreq := &jsonData{
		Type:  docType,
		ID:    id,
		Attrs: attrs,
	}

	if parent != nil {
		bodyreq.Rels = map[string]interface{}{
			"parent": map[string]interface{}{
				"data": parent,
			},
		}
	}

	b, err := json.Marshal(map[string]*jsonData{"data": bodyreq})
	if !assert.NoError(t, err) {
		return
	}

	req, err := http.NewRequest("PATCH", ts.URL+path, bytes.NewReader(b))
	req.Header.Add(echo.HeaderAuthorization, "Bearer "+token)
	if !assert.NoError(t, err) {
		return
	}

	res, err = http.DefaultClient.Do(req)
	if !assert.NoError(t, err) {
		return
	}

	defer res.Body.Close()

	err = extractJSONRes(res, &v)
	assert.NoError(t, err)

	return
}

func download(t *testing.T, path, byteRange string) (res *http.Response, body []byte) {
	req, err := http.NewRequest("GET", ts.URL+path, nil)
	req.Header.Add(echo.HeaderAuthorization, "Bearer "+token)
	if !assert.NoError(t, err) {
		return
	}

	if byteRange != "" {
		req.Header.Add("Range", byteRange)
	}

	res, err = http.DefaultClient.Do(req)
	if !assert.NoError(t, err) {
		return
	}

	body, err = ioutil.ReadAll(res.Body)
	if !assert.NoError(t, err) {
		return
	}

	return
}

func TestChanges(t *testing.T) {
	_, foo := createDir(t, "/files/?Name=foo&Type=directory")
	fooID := foo["data"].(map[string]interface{})["id"].(string)
	_, bar := createDir(t, "/files/"+fooID+"?Name=bar&Type=directory")
	barID := bar["data"].(map[string]interface{})["id"].(string)
	_, _ = upload(t, "/files/"+barID+"?Type=file&Name=baz", "text/plain", "baz", "")

	_, qux := createDir(t, "/files/?Name=qux&Type=directory")
	quxID := qux["data"].(map[string]interface{})["id"].(string)
	_, _ = trash(t, "/files/"+quxID)
	req, err := http.NewRequest(http.MethodDelete, ts.URL+"/files/trash", nil)
	assert.NoError(t, err)
	req.Header.Add(echo.HeaderAuthorization, "Bearer "+token)
	_, err = http.DefaultClient.Do(req)
	assert.NoError(t, err)

	req, _ = http.NewRequest("GET", ts.URL+"/files/_changes?include_docs=true&include_file_path=true", nil)
	req.Header.Add(echo.HeaderAuthorization, "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	assert.Equal(t, 200, res.StatusCode)

	var obj map[string]interface{}
	err = extractJSONRes(res, &obj)
	assert.NoError(t, err)
	assert.NotEmpty(t, obj["last_seq"])
	assert.NotNil(t, obj["pending"])
	results := obj["results"].([]interface{})
	hasDeleted := false
	hasTrashed := false
	for _, result := range results {
		result, _ := result.(map[string]interface{})
		assert.NotEmpty(t, result["id"])
		if result["deleted"] == true {
			hasDeleted = true
		} else {
			doc, _ := result["doc"].(map[string]interface{})
			assert.NotEmpty(t, doc["type"])
			assert.NotEmpty(t, doc["path"])
			if doc["path"] == "/.cozy_trash" {
				hasTrashed = true
			}
		}
	}
	assert.True(t, hasDeleted)
	assert.True(t, hasTrashed)

	req, _ = http.NewRequest("GET", ts.URL+"/files/_changes?include_docs=true&fields=type,name,dir_id", nil)
	req.Header.Add(echo.HeaderAuthorization, "Bearer "+token)
	res, err = http.DefaultClient.Do(req)
	assert.NoError(t, err)
	assert.Equal(t, 200, res.StatusCode)

	err = extractJSONRes(res, &obj)
	assert.NoError(t, err)
	assert.NotEmpty(t, obj["last_seq"])
	assert.NotNil(t, obj["pending"])
	results = obj["results"].([]interface{})
	for _, result := range results {
		result, _ := result.(map[string]interface{})
		assert.NotEmpty(t, result["id"])
		if result["deleted"] != true && result["id"] != "io.cozy.files.root-dir" {
			doc, _ := result["doc"].(map[string]interface{})
			assert.NotEmpty(t, doc["type"])
			assert.NotEmpty(t, doc["name"])
			assert.NotEmpty(t, doc["dir_id"])
			assert.Empty(t, doc["path"])
			assert.Empty(t, doc["metadata"])
			assert.Empty(t, doc["created_at"])
		}
	}

	_, _ = trash(t, "/files/"+barID)
	req, _ = http.NewRequest("GET", ts.URL+"/files/_changes?include_docs=true&skip_deleted=true&skip_trashed=true", nil)
	req.Header.Add(echo.HeaderAuthorization, "Bearer "+token)
	res, err = http.DefaultClient.Do(req)
	assert.NoError(t, err)
	assert.Equal(t, 200, res.StatusCode)

	err = extractJSONRes(res, &obj)
	assert.NoError(t, err)
	assert.NotEmpty(t, obj["last_seq"])
	assert.NotNil(t, obj["pending"])
	results = obj["results"].([]interface{})
	hasDeleted = false
	hasTrashed = false
	for _, result := range results {
		result, _ := result.(map[string]interface{})
		assert.NotEmpty(t, result["id"])
		if result["deleted"] == true {
			hasDeleted = true
		} else {
			doc, _ := result["doc"].(map[string]interface{})
			assert.NotEmpty(t, doc["type"])
			assert.NotEmpty(t, doc["path"])
			if doc["path"] == "/.cozy_trash" {
				hasTrashed = true
			}
			if doc["type"] == "directory" && strings.HasPrefix(doc["path"].(string), "/.cozy_trash") {
				hasTrashed = true
			}
			if doc["type"] == "file" && doc["trashed"] == true {
				hasTrashed = true
			}
		}
	}
	assert.False(t, hasDeleted)
	assert.False(t, hasTrashed)

	_, _ = trash(t, "/files/"+fooID)
	req, err = http.NewRequest(http.MethodDelete, ts.URL+"/files/trash", nil)
	assert.NoError(t, err)
	req.Header.Add(echo.HeaderAuthorization, "Bearer "+token)
	_, err = http.DefaultClient.Do(req)
	assert.NoError(t, err)
}

func TestCreateDirWithNoType(t *testing.T) {
	res, _ := createDir(t, "/files/")
	assert.Equal(t, 422, res.StatusCode)
}

func TestCreateDirWithNoName(t *testing.T) {
	res, _ := createDir(t, "/files/?Type=directory")
	assert.Equal(t, 422, res.StatusCode)
}

func TestCreateDirOnNonExistingParent(t *testing.T) {
	res, _ := createDir(t, "/files/noooop?Name=foo&Type=directory")
	assert.Equal(t, 404, res.StatusCode)
}

func TestCreateDirAlreadyExists(t *testing.T) {
	res1, _ := createDir(t, "/files/?Name=iexist&Type=directory")
	assert.Equal(t, 201, res1.StatusCode)

	res2, _ := createDir(t, "/files/?Name=iexist&Type=directory")
	assert.Equal(t, 409, res2.StatusCode)
}

func TestCreateDirRootSuccess(t *testing.T) {
	res, _ := createDir(t, "/files/?Name=coucou&Type=directory")
	assert.Equal(t, 201, res.StatusCode)

	storage := testInstance.VFS()
	exists, err := vfs.DirExists(storage, "/coucou")
	assert.NoError(t, err)
	assert.True(t, exists)
}

func TestCreateDirWithDateSuccess(t *testing.T) {
	req, _ := http.NewRequest("POST", ts.URL+"/files/?Type=directory&Name=dir-with-date&CreatedAt=2016-09-18T10:24:53Z", strings.NewReader(""))
	req.Header.Add(echo.HeaderAuthorization, "Bearer "+token)
	req.Header.Add("Date", "Mon, 19 Sep 2016 12:35:08 GMT")
	res, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	assert.Equal(t, 201, res.StatusCode)

	var obj map[string]interface{}
	err = extractJSONRes(res, &obj)
	assert.NoError(t, err)
	data := obj["data"].(map[string]interface{})
	attrs := data["attributes"].(map[string]interface{})
	createdAt := attrs["created_at"].(string)
	assert.Equal(t, "2016-09-18T10:24:53Z", createdAt)
	updatedAt := attrs["updated_at"].(string)
	assert.Equal(t, "2016-09-19T12:35:08Z", updatedAt)
	fcm := attrs["cozyMetadata"].(map[string]interface{})
	assert.Equal(t, float64(1), fcm["metadataVersion"])
	assert.Equal(t, "1", fcm["doctypeVersion"])
	assert.Contains(t, fcm["createdOn"], testInstance.Domain)
	assert.NotEmpty(t, fcm["createdAt"])
	assert.NotEmpty(t, fcm["updatedAt"])
	assert.NotContains(t, fcm, "uploadedAt")
}

func TestCreateDirWithDateSuccessAndUpdatedAt(t *testing.T) {
	req, _ := http.NewRequest("POST", ts.URL+"/files/?Type=directory&Name=dir-with-date-and-updatedat&CreatedAt=2016-09-18T10:24:53Z&UpdatedAt=2020-05-12T12:25:00Z", strings.NewReader(""))
	req.Header.Add(echo.HeaderAuthorization, "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	assert.Equal(t, 201, res.StatusCode)

	var obj map[string]interface{}
	err = extractJSONRes(res, &obj)
	assert.NoError(t, err)
	data := obj["data"].(map[string]interface{})
	attrs := data["attributes"].(map[string]interface{})
	createdAt := attrs["created_at"].(string)
	assert.Equal(t, "2016-09-18T10:24:53Z", createdAt)
	updatedAt := attrs["updated_at"].(string)
	assert.Equal(t, "2020-05-12T12:25:00Z", updatedAt)
	fcm := attrs["cozyMetadata"].(map[string]interface{})
	assert.Equal(t, float64(1), fcm["metadataVersion"])
	assert.Equal(t, "1", fcm["doctypeVersion"])
	assert.Contains(t, fcm["createdOn"], testInstance.Domain)
	assert.NotEmpty(t, fcm["createdAt"])
	assert.NotEmpty(t, fcm["updatedAt"])
	assert.NotContains(t, fcm, "uploadedAt")
}
func TestCreateDirWithParentSuccess(t *testing.T) {
	res1, data1 := createDir(t, "/files/?Name=dirparent&Type=directory")
	assert.Equal(t, 201, res1.StatusCode)

	var ok bool
	data1, ok = data1["data"].(map[string]interface{})
	assert.True(t, ok)

	parentID, ok := data1["id"].(string)
	assert.True(t, ok)

	res2, _ := createDir(t, "/files/"+parentID+"?Name=child&Type=directory")
	assert.Equal(t, 201, res2.StatusCode)

	storage := testInstance.VFS()
	exists, err := vfs.DirExists(storage, "/dirparent/child")
	assert.NoError(t, err)
	assert.True(t, exists)
}

func TestCreateDirWithIllegalCharacter(t *testing.T) {
	res1, _ := createDir(t, "/files/?Name=coucou/les/copains!&Type=directory")
	assert.Equal(t, 422, res1.StatusCode)
}

func TestCreateDirConcurrently(t *testing.T) {
	done := make(chan *http.Response)
	errs := make(chan *http.Response)

	doCreateDir := func(name string) {
		res, _ := createDir(t, "/files/?Name="+name+"&Type=directory")
		if res.StatusCode == 201 {
			done <- res
		} else {
			errs <- res
		}
	}

	n := 100
	c := 0

	for i := 0; i < n; i++ {
		go doCreateDir("foo")
	}

	for i := 0; i < n; i++ {
		select {
		case res := <-errs:
			assert.True(t, res.StatusCode == 409 || res.StatusCode == 503)
		case <-done:
			c = c + 1
		}
	}

	assert.Equal(t, 1, c)
}

func TestUploadWithNoType(t *testing.T) {
	res, _ := upload(t, "/files/", "text/plain", "foo", "")
	assert.Equal(t, 422, res.StatusCode)
}

func TestUploadWithNoName(t *testing.T) {
	res, _ := upload(t, "/files/?Type=file", "text/plain", "foo", "")
	assert.Equal(t, 422, res.StatusCode)
}

func TestUploadToNonExistingParent(t *testing.T) {
	res, _ := upload(t, "/files/nooop?Type=file&Name=no-parent", "text/plain", "foo", "")
	assert.Equal(t, 404, res.StatusCode)
}

func TestUploadWithInvalidContentType(t *testing.T) {
	res, _ := upload(t, "/files/?Type=file&Name=InvalidMime", "foo € / bar", "foo", "")
	assert.Equal(t, 422, res.StatusCode)
}

func TestUploadToTrashedFolder(t *testing.T) {
	res1, data1 := createDir(t, "/files/?Name=trashed-parent&Type=directory")
	assert.Equal(t, 201, res1.StatusCode)
	dirID, _ := extractDirData(t, data1)
	res2, _ := trash(t, "/files/"+dirID)
	assert.Equal(t, 200, res2.StatusCode)
	res3, _ := upload(t, "/files/"+dirID+"?Type=file&Name=trashed-parent", "text/plain", "foo", "")
	assert.Equal(t, 404, res3.StatusCode)
}

func TestUploadBadSize(t *testing.T) {
	body := "foo"
	res, _ := upload(t, "/files/?Type=file&Name=badsize&Size=42", "text/plain", body, "")
	assert.Equal(t, 412, res.StatusCode)

	storage := testInstance.VFS()
	_, err := readFile(storage, "/badsize")
	assert.Error(t, err)
}

func TestUploadBadHash(t *testing.T) {
	body := "foo"
	res, _ := upload(t, "/files/?Type=file&Name=badhash", "text/plain", body, "3FbbMXfH+PdjAlWFfVb1dQ==")
	assert.Equal(t, 412, res.StatusCode)

	storage := testInstance.VFS()
	_, err := readFile(storage, "/badhash")
	assert.Error(t, err)
}

func TestUploadAtRootSuccess(t *testing.T) {
	body := "foo"
	res, _ := upload(t, "/files/?Type=file&Name=goodhash", "text/plain", body, "rL0Y20zC+Fzt72VPzMSk2A==")
	assert.Equal(t, 201, res.StatusCode)

	storage := testInstance.VFS()
	buf, err := readFile(storage, "/goodhash")
	assert.NoError(t, err)
	assert.Equal(t, body, string(buf))
}

func TestUploadImage(t *testing.T) {
	f, err := os.Open("../../tests/fixtures/wet-cozy_20160910__M4Dz.jpg")
	assert.NoError(t, err)
	defer f.Close()
	m := `{"gps":{"city":"Paris","country":"France"}}`
	req, err := http.NewRequest("POST", ts.URL+"/files/?Type=file&Name=wet.jpg&Metadata="+m, f)
	assert.NoError(t, err)
	req.Header.Add(echo.HeaderAuthorization, "Bearer "+token)
	res, obj := doUploadOrMod(t, req, "image/jpeg", "tHWYYuXBBflJ8wXgJ2c2yg==")
	assert.Equal(t, 201, res.StatusCode)
	data := obj["data"].(map[string]interface{})
	imgID = data["id"].(string)
	attrs := data["attributes"].(map[string]interface{})
	meta := attrs["metadata"].(map[string]interface{})
	v := meta["extractor_version"].(float64)
	assert.Equal(t, float64(vfs.MetadataExtractorVersion), v)
	flash := meta["flash"].(string)
	assert.Equal(t, "Off, Did not fire", flash)
	gps := meta["gps"].(map[string]interface{})
	assert.Equal(t, "Paris", gps["city"])
	assert.Equal(t, "France", gps["country"])
	assert.Contains(t, attrs["created_at"], "2016-09-10T")
}

func TestUploadShortcut(t *testing.T) {
	f, err := os.Open("../../tests/fixtures/shortcut.url")
	assert.NoError(t, err)
	defer f.Close()
	req, err := http.NewRequest("POST", ts.URL+"/files/?Type=file&Name=shortcut.url", f)
	assert.NoError(t, err)
	req.Header.Add(echo.HeaderAuthorization, "Bearer "+token)
	res, obj := doUploadOrMod(t, req, "application/octet-stream", "+tHtr9V8+4gcCDxTFAqt3w==")
	assert.Equal(t, 201, res.StatusCode)
	data := obj["data"].(map[string]interface{})
	attrs := data["attributes"].(map[string]interface{})
	assert.Equal(t, "application/internet-shortcut", attrs["mime"])
	assert.Equal(t, "shortcut", attrs["class"])
}

func TestUploadWithParentSuccess(t *testing.T) {
	res1, data1 := createDir(t, "/files/?Name=fileparent&Type=directory")
	assert.Equal(t, 201, res1.StatusCode)

	var ok bool
	data1, ok = data1["data"].(map[string]interface{})
	assert.True(t, ok)

	parentID, ok := data1["id"].(string)
	assert.True(t, ok)

	body := "foo"
	res2, _ := upload(t, "/files/"+parentID+"?Type=file&Name=goodhash", "text/plain", body, "rL0Y20zC+Fzt72VPzMSk2A==")
	assert.Equal(t, 201, res2.StatusCode)

	storage := testInstance.VFS()
	buf, err := readFile(storage, "/fileparent/goodhash")
	assert.NoError(t, err)
	assert.Equal(t, body, string(buf))
}

func TestUploadAtRootAlreadyExists(t *testing.T) {
	body := "foo"
	res1, _ := upload(t, "/files/?Type=file&Name=iexistfile", "text/plain", body, "rL0Y20zC+Fzt72VPzMSk2A==")
	assert.Equal(t, 201, res1.StatusCode)

	res2, _ := upload(t, "/files/?Type=file&Name=iexistfile", "text/plain", body, "rL0Y20zC+Fzt72VPzMSk2A==")
	assert.Equal(t, 409, res2.StatusCode)
}

func TestUploadWithParentAlreadyExists(t *testing.T) {
	_, dirdata := createDir(t, "/files/?Type=directory&Name=container")

	var ok bool
	dirdata, ok = dirdata["data"].(map[string]interface{})
	assert.True(t, ok)

	parentID, ok := dirdata["id"].(string)
	assert.True(t, ok)

	body := "foo"
	res1, _ := upload(t, "/files/"+parentID+"?Type=file&Name=iexistfile", "text/plain", body, "rL0Y20zC+Fzt72VPzMSk2A==")
	assert.Equal(t, 201, res1.StatusCode)

	res2, _ := upload(t, "/files/"+parentID+"?Type=file&Name=iexistfile", "text/plain", body, "rL0Y20zC+Fzt72VPzMSk2A==")
	assert.Equal(t, 409, res2.StatusCode)
}

func TestUploadWithCreatedAtAndHeaderDate(t *testing.T) {
	buf := strings.NewReader("foo")
	req, err := http.NewRequest("POST", ts.URL+"/files/?Type=file&Name=withcdate&CreatedAt=2016-09-18T10:24:53Z", buf)
	req.Header.Add(echo.HeaderAuthorization, "Bearer "+token)
	assert.NoError(t, err)
	req.Header.Add("Date", "Mon, 19 Sep 2016 12:38:04 GMT")
	res, obj := doUploadOrMod(t, req, "text/plain", "rL0Y20zC+Fzt72VPzMSk2A==")
	assert.Equal(t, 201, res.StatusCode)
	data := obj["data"].(map[string]interface{})
	attrs := data["attributes"].(map[string]interface{})
	createdAt := attrs["created_at"].(string)
	assert.Equal(t, "2016-09-18T10:24:53Z", createdAt)
	updatedAt := attrs["updated_at"].(string)
	assert.Equal(t, "2016-09-19T12:38:04Z", updatedAt)
}

func TestUploadWithCreatedAtAndUpdatedAt(t *testing.T) {
	buf := strings.NewReader("foo")
	req, err := http.NewRequest("POST", ts.URL+"/files/?Type=file&Name=TestUploadWithCreatedAtAndUpdatedAt&CreatedAt=2016-09-18T10:24:53Z&UpdatedAt=2020-05-12T12:25:00Z", buf)
	req.Header.Add(echo.HeaderAuthorization, "Bearer "+token)
	assert.NoError(t, err)
	res, obj := doUploadOrMod(t, req, "text/plain", "rL0Y20zC+Fzt72VPzMSk2A==")
	assert.Equal(t, 201, res.StatusCode)
	data := obj["data"].(map[string]interface{})
	attrs := data["attributes"].(map[string]interface{})
	createdAt := attrs["created_at"].(string)
	assert.Equal(t, "2016-09-18T10:24:53Z", createdAt)
	updatedAt := attrs["updated_at"].(string)
	assert.Equal(t, "2020-05-12T12:25:00Z", updatedAt)
}

func TestUploadWithCreatedAtAndUpdatedAtAndDateHeader(t *testing.T) {
	buf := strings.NewReader("foo")
	req, err := http.NewRequest("POST", ts.URL+"/files/?Type=file&Name=TestUploadWithCreatedAtAndUpdatedAtAndDateHeader&CreatedAt=2016-09-18T10:24:53Z&UpdatedAt=2020-05-12T12:25:00Z", buf)
	req.Header.Add(echo.HeaderAuthorization, "Bearer "+token)
	assert.NoError(t, err)
	req.Header.Add("Date", "Mon, 19 Sep 2016 12:38:04 GMT")
	res, obj := doUploadOrMod(t, req, "text/plain", "rL0Y20zC+Fzt72VPzMSk2A==")
	assert.Equal(t, 201, res.StatusCode)
	data := obj["data"].(map[string]interface{})
	attrs := data["attributes"].(map[string]interface{})
	createdAt := attrs["created_at"].(string)
	assert.Equal(t, "2016-09-18T10:24:53Z", createdAt)
	updatedAt := attrs["updated_at"].(string)
	assert.Equal(t, "2020-05-12T12:25:00Z", updatedAt)
}

func TestUploadWithMetadata(t *testing.T) {
	buf := strings.NewReader(`{
    "data": {
        "type": "io.cozy.files.metadata",
        "attributes": {
            "category": "report",
            "subCategory": "theft",
            "datetime": "2017-04-22T01:00:00-05:00",
            "label": "foobar"
        }
    }
}`)
	req, err := http.NewRequest("POST", ts.URL+"/files/upload/metadata", buf)
	req.Header.Add(echo.HeaderAuthorization, "Bearer "+token)
	assert.NoError(t, err)
	res, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	defer res.Body.Close()
	assert.Equal(t, 201, res.StatusCode)
	var obj map[string]interface{}
	err = extractJSONRes(res, &obj)
	assert.NoError(t, err)
	data := obj["data"].(map[string]interface{})
	assert.Equal(t, consts.FilesMetadata, data["type"])
	secret := data["id"].(string)
	attrs := data["attributes"].(map[string]interface{})
	assert.Equal(t, "report", attrs["category"])
	assert.Equal(t, "theft", attrs["subCategory"])
	assert.Equal(t, "foobar", attrs["label"])
	assert.Equal(t, "2017-04-22T01:00:00-05:00", attrs["datetime"])

	u := "/files/?Type=file&Name=withmetadataid&MetadataID=" + secret
	buf = strings.NewReader("foo")
	req, err = http.NewRequest("POST", ts.URL+u, buf)
	req.Header.Add(echo.HeaderAuthorization, "Bearer "+token)
	assert.NoError(t, err)
	res, obj = doUploadOrMod(t, req, "text/plain", "rL0Y20zC+Fzt72VPzMSk2A==")
	assert.Equal(t, 201, res.StatusCode)
	data = obj["data"].(map[string]interface{})
	attrs = data["attributes"].(map[string]interface{})
	meta := attrs["metadata"].(map[string]interface{})
	assert.Equal(t, "report", meta["category"])
	assert.Equal(t, "theft", meta["subCategory"])
	assert.Equal(t, "foobar", meta["label"])
	assert.Equal(t, "2017-04-22T01:00:00-05:00", meta["datetime"])
}

func TestUploadWithSourceAccount(t *testing.T) {
	buf := strings.NewReader("foo")
	account := "0c5a0a1e-8eb1-11e9-93f3-934f3a2c181d"
	identifier := "11f68e48"
	req, err := http.NewRequest("POST", ts.URL+"/files/?Type=file&Name=with-sourceAccount&SourceAccount="+account+"&SourceAccountIdentifier="+identifier, buf)
	req.Header.Add(echo.HeaderAuthorization, "Bearer "+token)
	assert.NoError(t, err)
	res, obj := doUploadOrMod(t, req, "text/plain", "rL0Y20zC+Fzt72VPzMSk2A==")
	assert.Equal(t, 201, res.StatusCode)
	data := obj["data"].(map[string]interface{})
	attrs := data["attributes"].(map[string]interface{})
	fcm := attrs["cozyMetadata"].(map[string]interface{})
	assert.Equal(t, account, fcm["sourceAccount"])
	assert.Equal(t, identifier, fcm["sourceAccountIdentifier"])
}

func TestModifyMetadataByPath(t *testing.T) {
	body := "foo"
	res1, data1 := upload(t, "/files/?Type=file&Name=file-move-me-by-path", "text/plain", body, "rL0Y20zC+Fzt72VPzMSk2A==")
	assert.Equal(t, 201, res1.StatusCode)

	var ok bool
	data1, ok = data1["data"].(map[string]interface{})
	assert.True(t, ok)

	fileID, ok := data1["id"].(string)
	assert.True(t, ok)

	res2, data2 := createDir(t, "/files/?Name=move-by-path&Type=directory")
	assert.Equal(t, 201, res2.StatusCode)

	data2, ok = data2["data"].(map[string]interface{})
	assert.True(t, ok)

	dirID, ok := data2["id"].(string)
	assert.True(t, ok)

	attrs := map[string]interface{}{
		"tags":       []string{"bar", "bar", "baz"},
		"name":       "moved",
		"dir_id":     dirID,
		"executable": true,
	}

	res3, data3 := patchFile(t, "/files/metadata?Path=/file-move-me-by-path", "file", fileID, attrs, nil)
	assert.Equal(t, 200, res3.StatusCode)

	data3, ok = data3["data"].(map[string]interface{})
	assert.True(t, ok)

	attrs3, ok := data3["attributes"].(map[string]interface{})
	assert.True(t, ok)

	assert.Equal(t, "text/plain", attrs3["mime"])
	assert.Equal(t, "moved", attrs3["name"])
	assert.EqualValues(t, []interface{}{"bar", "baz"}, attrs3["tags"])
	assert.Equal(t, "text", attrs3["class"])
	assert.Equal(t, "rL0Y20zC+Fzt72VPzMSk2A==", attrs3["md5sum"])
	assert.Equal(t, true, attrs3["executable"])
	assert.Equal(t, "3", attrs3["size"])
}

func TestModifyMetadataFileMove(t *testing.T) {
	body := "foo"
	res1, data1 := upload(t, "/files/?Type=file&Name=filemoveme&Tags=foo,bar", "text/plain", body, "rL0Y20zC+Fzt72VPzMSk2A==")
	assert.Equal(t, 201, res1.StatusCode)

	var ok bool
	data1, ok = data1["data"].(map[string]interface{})
	assert.True(t, ok)

	fileID, ok := data1["id"].(string)
	assert.True(t, ok)

	res2, data2 := createDir(t, "/files/?Name=movemeinme&Type=directory")
	assert.Equal(t, 201, res2.StatusCode)

	data2, ok = data2["data"].(map[string]interface{})
	assert.True(t, ok)

	dirID, ok := data2["id"].(string)
	assert.True(t, ok)

	attrs := map[string]interface{}{
		"tags":       []string{"bar", "bar", "baz"},
		"name":       "moved",
		"dir_id":     dirID,
		"executable": true,
	}

	res3, data3 := patchFile(t, "/files/"+fileID, "file", fileID, attrs, nil)
	assert.Equal(t, 200, res3.StatusCode)

	data3, ok = data3["data"].(map[string]interface{})
	assert.True(t, ok)

	attrs3, ok := data3["attributes"].(map[string]interface{})
	assert.True(t, ok)

	assert.Equal(t, "text/plain", attrs3["mime"])
	assert.Equal(t, "moved", attrs3["name"])
	assert.EqualValues(t, []interface{}{"bar", "baz"}, attrs3["tags"])
	assert.Equal(t, "text", attrs3["class"])
	assert.Equal(t, "rL0Y20zC+Fzt72VPzMSk2A==", attrs3["md5sum"])
	assert.Equal(t, true, attrs3["executable"])
	assert.Equal(t, "3", attrs3["size"])
}

func TestModifyMetadataFileConflict(t *testing.T) {
	body := "foo"
	res1, data1 := upload(t, "/files/?Type=file&Name=fmodme1&Tags=foo,bar", "text/plain", body, "rL0Y20zC+Fzt72VPzMSk2A==")
	assert.Equal(t, 201, res1.StatusCode)

	res2, _ := upload(t, "/files/?Type=file&Name=fmodme2&Tags=foo,bar", "text/plain", body, "rL0Y20zC+Fzt72VPzMSk2A==")
	assert.Equal(t, 201, res2.StatusCode)

	file1ID, _ := extractDirData(t, data1)

	attrs := map[string]interface{}{
		"name": "fmodme2",
	}

	res3, _ := patchFile(t, "/files/"+file1ID, "file", file1ID, attrs, nil)
	assert.Equal(t, 409, res3.StatusCode)
}

func TestModifyMetadataDirMove(t *testing.T) {
	res1, data1 := createDir(t, "/files/?Name=dirmodme&Type=directory&Tags=foo,bar,bar")
	assert.Equal(t, 201, res1.StatusCode)

	dir1ID, _ := extractDirData(t, data1)

	reschild1, _ := createDir(t, "/files/"+dir1ID+"?Name=child1&Type=directory")
	assert.Equal(t, 201, reschild1.StatusCode)

	reschild2, _ := createDir(t, "/files/"+dir1ID+"?Name=child2&Type=directory")
	assert.Equal(t, 201, reschild2.StatusCode)

	res2, data2 := createDir(t, "/files/?Name=dirmodmemoveinme&Type=directory")
	assert.Equal(t, 201, res2.StatusCode)

	dir2ID, _ := extractDirData(t, data2)

	attrs1 := map[string]interface{}{
		"tags":   []string{"bar", "baz"},
		"name":   "renamed",
		"dir_id": dir2ID,
	}

	res3, _ := patchFile(t, "/files/"+dir1ID, "directory", dir1ID, attrs1, nil)
	assert.Equal(t, 200, res3.StatusCode)

	storage := testInstance.VFS()
	exists, err := vfs.DirExists(storage, "/dirmodmemoveinme/renamed")
	assert.NoError(t, err)
	assert.True(t, exists)

	attrs2 := map[string]interface{}{
		"tags":   []string{"bar", "baz"},
		"name":   "renamed",
		"dir_id": dir1ID,
	}

	res4, _ := patchFile(t, "/files/"+dir2ID, "directory", dir2ID, attrs2, nil)
	assert.Equal(t, 412, res4.StatusCode)

	res5, _ := patchFile(t, "/files/"+dir1ID, "directory", dir1ID, attrs2, nil)
	assert.Equal(t, 412, res5.StatusCode)
}

func TestModifyMetadataDirMoveWithRel(t *testing.T) {
	res1, data1 := createDir(t, "/files/?Name=dirmodmewithrel&Type=directory&Tags=foo,bar,bar")
	assert.Equal(t, 201, res1.StatusCode)

	dir1ID, _ := extractDirData(t, data1)

	reschild1, datachild1 := createDir(t, "/files/"+dir1ID+"?Name=child1&Type=directory")
	assert.Equal(t, 201, reschild1.StatusCode)

	reschild2, datachild2 := createDir(t, "/files/"+dir1ID+"?Name=child2&Type=directory")
	assert.Equal(t, 201, reschild2.StatusCode)

	res2, data2 := createDir(t, "/files/?Name=dirmodmemoveinmewithrel&Type=directory")
	assert.Equal(t, 201, res2.StatusCode)

	dir2ID, _ := extractDirData(t, data2)
	extractDirData(t, datachild1)
	extractDirData(t, datachild2)

	parent := &jsonData{
		ID:   dir2ID,
		Type: "io.cozy.files",
	}

	res3, _ := patchFile(t, "/files/"+dir1ID, "directory", dir1ID, nil, parent)
	assert.Equal(t, 200, res3.StatusCode)

	storage := testInstance.VFS()
	exists, err := vfs.DirExists(storage, "/dirmodmemoveinmewithrel/dirmodmewithrel")
	assert.NoError(t, err)
	assert.True(t, exists)
}

func TestModifyMetadataDirMoveConflict(t *testing.T) {
	res1, _ := createDir(t, "/files/?Name=conflictmodme1&Type=directory&Tags=foo,bar,bar")
	assert.Equal(t, 201, res1.StatusCode)

	res2, data2 := createDir(t, "/files/?Name=conflictmodme2&Type=directory")
	assert.Equal(t, 201, res2.StatusCode)

	dir2ID, _ := extractDirData(t, data2)

	attrs1 := map[string]interface{}{
		"tags": []string{"bar", "baz"},
		"name": "conflictmodme1",
	}

	res3, _ := patchFile(t, "/files/"+dir2ID, "directory", dir2ID, attrs1, nil)
	assert.Equal(t, 409, res3.StatusCode)
}

func TestModifyContentNoFileID(t *testing.T) {
	res, _ := uploadMod(t, "/files/badid", "text/plain", "nil", "")
	assert.Equal(t, 404, res.StatusCode)
}

func TestModifyContentBadRev(t *testing.T) {
	res1, data1 := upload(t, "/files/?Type=file&Name=modbadrev&Executable=true", "text/plain", "foo", "")
	assert.Equal(t, 201, res1.StatusCode)

	var ok bool
	data1, ok = data1["data"].(map[string]interface{})
	assert.True(t, ok)

	meta1, ok := data1["meta"].(map[string]interface{})
	assert.True(t, ok)

	fileID, ok := data1["id"].(string)
	assert.True(t, ok)
	fileRev, ok := meta1["rev"].(string)
	assert.True(t, ok)

	newcontent := "newcontent :)"

	req2, err := http.NewRequest("PUT", ts.URL+"/files/"+fileID, strings.NewReader(newcontent))
	req2.Header.Add(echo.HeaderAuthorization, "Bearer "+token)
	assert.NoError(t, err)

	req2.Header.Add("If-Match", "badrev")
	res2, _ := doUploadOrMod(t, req2, "text/plain", "")
	assert.Equal(t, 412, res2.StatusCode)

	req3, err := http.NewRequest("PUT", ts.URL+"/files/"+fileID, strings.NewReader(newcontent))
	req3.Header.Add(echo.HeaderAuthorization, "Bearer "+token)
	assert.NoError(t, err)

	req3.Header.Add("If-Match", fileRev)
	res3, _ := doUploadOrMod(t, req3, "text/plain", "")
	assert.Equal(t, 200, res3.StatusCode)
}

func TestModifyContentSuccess(t *testing.T) {
	var err error
	var buf []byte
	var fileInfo os.FileInfo

	storage := testInstance.VFS()
	res1, data1 := upload(t, "/files/?Type=file&Name=willbemodified&Executable=true", "text/plain", "foo", "")
	assert.Equal(t, 201, res1.StatusCode)

	buf, err = readFile(storage, "/willbemodified")
	assert.NoError(t, err)
	assert.Equal(t, "foo", string(buf))
	fileInfo, err = storage.FileByPath("/willbemodified")
	assert.NoError(t, err)
	assert.Equal(t, fileInfo.Mode().String(), "-rwxr-xr-x")

	var ok bool
	data1, ok = data1["data"].(map[string]interface{})
	assert.True(t, ok)

	attrs1, ok := data1["attributes"].(map[string]interface{})
	assert.True(t, ok)

	links1, ok := data1["links"].(map[string]interface{})
	assert.True(t, ok)

	fileID, ok := data1["id"].(string)
	assert.True(t, ok)

	newcontent := "newcontent :)"
	res2, data2 := uploadMod(t, "/files/"+fileID+"?Executable=false", "audio/mp3", newcontent, "")
	assert.Equal(t, 200, res2.StatusCode)

	data2, ok = data2["data"].(map[string]interface{})
	assert.True(t, ok)

	meta2, ok := data2["meta"].(map[string]interface{})
	assert.True(t, ok)

	attrs2, ok := data2["attributes"].(map[string]interface{})
	assert.True(t, ok)

	links2, ok := data2["links"].(map[string]interface{})
	assert.True(t, ok)

	assert.Equal(t, data2["id"], data1["id"], "same id")
	assert.Equal(t, data2["path"], data1["path"], "same path")
	assert.NotEqual(t, meta2["rev"], data1["rev"], "different rev")
	assert.Equal(t, links2["self"], links1["self"], "same self link")

	assert.Equal(t, attrs2["name"], attrs1["name"])
	assert.Equal(t, attrs2["created_at"], attrs1["created_at"])
	assert.NotEqual(t, attrs2["updated_at"], attrs1["updated_at"])
	assert.NotEqual(t, attrs2["size"], attrs1["size"])

	assert.Equal(t, attrs2["size"], strconv.Itoa(len(newcontent)))
	assert.NotEqual(t, attrs2["md5sum"], attrs1["md5sum"])
	assert.NotEqual(t, attrs2["class"], attrs1["class"])
	assert.NotEqual(t, attrs2["mime"], attrs1["mime"])
	assert.NotEqual(t, attrs2["executable"], attrs1["executable"])
	assert.Equal(t, attrs2["class"], "audio")
	assert.Equal(t, attrs2["mime"], "audio/mp3")
	assert.Equal(t, attrs2["executable"], false)

	buf, err = readFile(storage, "/willbemodified")
	assert.NoError(t, err)
	assert.Equal(t, newcontent, string(buf))
	fileInfo, err = storage.FileByPath("/willbemodified")
	assert.NoError(t, err)
	assert.Equal(t, fileInfo.Mode().String(), "-rw-r--r--")

	req, err := http.NewRequest("PUT", ts.URL+"/files/"+fileID, strings.NewReader(""))
	assert.NoError(t, err)

	req.Header.Add("Date", "Mon, 02 Jan 2006 15:04:05 MST")
	req.Header.Add(echo.HeaderAuthorization, "Bearer "+token)

	res3, data3 := doUploadOrMod(t, req, "what/ever", "")
	assert.Equal(t, 200, res3.StatusCode)

	data3, ok = data3["data"].(map[string]interface{})
	assert.True(t, ok)

	attrs3, ok := data3["attributes"].(map[string]interface{})
	assert.True(t, ok)

	assert.Equal(t, "2006-01-02T15:04:05Z", attrs3["updated_at"])

	newcontent = "encryptedcontent"
	res4, data4 := uploadMod(t, "/files/"+fileID+"?Encrypted=true", "audio/mp3", newcontent, "")
	assert.Equal(t, 200, res4.StatusCode)

	data4, ok = data4["data"].(map[string]interface{})
	assert.True(t, ok)
	attrs4, ok := data4["attributes"].(map[string]interface{})
	assert.True(t, ok)
	assert.Equal(t, attrs4["encrypted"], true)
}

func TestModifyContentWithSourceAccount(t *testing.T) {
	buf := strings.NewReader("foo")
	req, err := http.NewRequest("POST", ts.URL+"/files/?Type=file&Name=old-file-to-migrate", buf)
	req.Header.Add(echo.HeaderAuthorization, "Bearer "+token)
	assert.NoError(t, err)
	res, obj := doUploadOrMod(t, req, "text/plain", "rL0Y20zC+Fzt72VPzMSk2A==")
	assert.Equal(t, 201, res.StatusCode)
	data := obj["data"].(map[string]interface{})
	fileID, ok := data["id"].(string)
	assert.True(t, ok)

	account := "0c5a0a1e-8eb1-11e9-93f3-934f3a2c181d"
	identifier := "11f68e48"
	newcontent := "updated by a konnector to add the sourceAccount"
	res2, obj2 := uploadMod(t, "/files/"+fileID+"?SourceAccount="+account+"&SourceAccountIdentifier="+identifier, "text/plain", newcontent, "")
	assert.Equal(t, 200, res2.StatusCode)
	data2 := obj2["data"].(map[string]interface{})
	attrs2 := data2["attributes"].(map[string]interface{})
	fcm := attrs2["cozyMetadata"].(map[string]interface{})
	assert.Equal(t, account, fcm["sourceAccount"])
	assert.Equal(t, identifier, fcm["sourceAccountIdentifier"])
}

func TestModifyContentWithCreatedAt(t *testing.T) {
	buf := strings.NewReader("foo")
	req, err := http.NewRequest("POST", ts.URL+"/files/?Type=file&Name=old-file-with-c", buf)
	req.Header.Add(echo.HeaderAuthorization, "Bearer "+token)
	assert.NoError(t, err)
	res, obj := doUploadOrMod(t, req, "text/plain", "rL0Y20zC+Fzt72VPzMSk2A==")
	assert.Equal(t, 201, res.StatusCode)
	data := obj["data"].(map[string]interface{})
	fileID, ok := data["id"].(string)
	assert.True(t, ok)
	attrs := data["attributes"].(map[string]interface{})
	createdAt := attrs["created_at"].(string)
	updatedAt := attrs["updated_at"].(string)

	createdAt2 := "2017-11-16T13:37:01.345Z"
	newcontent := "updated by a client with a new CreatedAt"
	res2, obj2 := uploadMod(t, "/files/"+fileID+"?CreatedAt="+createdAt2, "text/plain", newcontent, "")
	assert.Equal(t, 200, res2.StatusCode)
	data2 := obj2["data"].(map[string]interface{})
	attrs2 := data2["attributes"].(map[string]interface{})
	createdAt3 := attrs2["created_at"].(string)
	updatedAt2 := attrs2["updated_at"].(string)
	assert.Equal(t, createdAt3, createdAt)
	assert.NotEqual(t, updatedAt2, updatedAt)
}

func TestModifyContentWithUpdatedAt(t *testing.T) {
	buf := strings.NewReader("foo")
	createdAt := "2017-11-16T13:37:01.345Z"
	req, err := http.NewRequest("POST", ts.URL+"/files/?Type=file&Name=old-file-with-u&CreatedAt="+createdAt, buf)
	req.Header.Add(echo.HeaderAuthorization, "Bearer "+token)
	assert.NoError(t, err)
	res, obj := doUploadOrMod(t, req, "text/plain", "rL0Y20zC+Fzt72VPzMSk2A==")
	assert.Equal(t, 201, res.StatusCode)
	data := obj["data"].(map[string]interface{})
	fileID, ok := data["id"].(string)
	assert.True(t, ok)

	updatedAt2 := "2017-12-16T13:37:01.345Z"
	newcontent := "updated by a client with a new UpdatedAt"
	res2, obj2 := uploadMod(t, "/files/"+fileID+"?UpdatedAt="+updatedAt2, "text/plain", newcontent, "")
	assert.Equal(t, 200, res2.StatusCode)
	data2 := obj2["data"].(map[string]interface{})
	attrs2 := data2["attributes"].(map[string]interface{})
	createdAt2 := attrs2["created_at"].(string)
	updatedAt3 := attrs2["updated_at"].(string)
	assert.Equal(t, createdAt2, createdAt)
	assert.Equal(t, updatedAt3, updatedAt2)
}

func TestModifyContentWithUpdatedAtAndCreatedAt(t *testing.T) {
	buf := strings.NewReader("foo")
	createdAt := "2017-11-16T13:37:01.345Z"
	req, err := http.NewRequest("POST", ts.URL+"/files/?Type=file&Name=old-file-with-u-and-c&CreatedAt="+createdAt, buf)
	req.Header.Add(echo.HeaderAuthorization, "Bearer "+token)
	assert.NoError(t, err)
	res, obj := doUploadOrMod(t, req, "text/plain", "rL0Y20zC+Fzt72VPzMSk2A==")
	assert.Equal(t, 201, res.StatusCode)
	data := obj["data"].(map[string]interface{})
	fileID, ok := data["id"].(string)
	assert.True(t, ok)

	createdAt2 := "2017-10-16T13:37:01.345Z"
	updatedAt2 := "2017-12-16T13:37:01.345Z"
	newcontent := "updated by a client with a CreatedAt older than UpdatedAt"
	res2, obj2 := uploadMod(t, "/files/"+fileID+"?CreatedAt="+createdAt2+"&UpdatedAt="+updatedAt2, "text/plain", newcontent, "")
	assert.Equal(t, 200, res2.StatusCode)
	data2 := obj2["data"].(map[string]interface{})
	attrs2 := data2["attributes"].(map[string]interface{})
	createdAt3 := attrs2["created_at"].(string)
	updatedAt3 := attrs2["updated_at"].(string)
	assert.Equal(t, createdAt3, createdAt)
	assert.Equal(t, updatedAt3, updatedAt2)
}

func TestModifyContentConcurrently(t *testing.T) {
	type result struct {
		rev string
		idx int64
	}

	done := make(chan *result)
	errs := make(chan *http.Response)

	res, data := upload(t, "/files/?Type=file&Name=willbemodifiedconcurrently&Executable=true", "text/plain", "foo", "")
	if !assert.Equal(t, 201, res.StatusCode) {
		return
	}

	var ok bool
	data, ok = data["data"].(map[string]interface{})
	assert.True(t, ok)

	fileID, ok := data["id"].(string)
	assert.True(t, ok)

	var c int64

	doModContent := func() {
		idx := atomic.AddInt64(&c, 1)
		res, data := uploadMod(t, "/files/"+fileID, "plain/text", "newcontent "+strconv.FormatInt(idx, 10), "")
		if res.StatusCode == 200 {
			data = data["data"].(map[string]interface{})
			meta := data["meta"].(map[string]interface{})
			done <- &result{meta["rev"].(string), idx}
		} else {
			errs <- res
		}
	}

	n := 100

	for i := 0; i < n; i++ {
		go doModContent()
	}

	var successes []*result
	for i := 0; i < n; i++ {
		select {
		case res := <-errs:
			assert.True(t, res.StatusCode == 409 || res.StatusCode == 503, "status code is %v and not 409 or 503", res.StatusCode)
		case res := <-done:
			successes = append(successes, res)
		}
	}

	assert.True(t, len(successes) >= 1, "there is at least one success")

	for i, s := range successes {
		assert.True(t, strings.HasPrefix(s.rev, strconv.Itoa(i+2)+"-"))
	}

	storage := testInstance.VFS()
	buf, err := readFile(storage, "/willbemodifiedconcurrently")
	assert.NoError(t, err)

	found := false
	for _, s := range successes {
		if string(buf) == "newcontent "+strconv.FormatInt(s.idx, 10) {
			found = true
			break
		}
	}

	assert.True(t, found)
}

func TestDownloadFileBadID(t *testing.T) {
	res, _ := download(t, "/files/download/badid", "")
	assert.Equal(t, 404, res.StatusCode)
}

func TestDownloadFileBadPath(t *testing.T) {
	res, _ := download(t, "/files/download?Path=/i/do/not/exist", "")
	assert.Equal(t, 404, res.StatusCode)
}

func TestDownloadFileByIDSuccess(t *testing.T) {
	body := "foo"
	res1, filedata := upload(t, "/files/?Type=file&Name=downloadme1", "text/plain", body, "rL0Y20zC+Fzt72VPzMSk2A==")
	assert.Equal(t, 201, res1.StatusCode)

	var ok bool
	filedata, ok = filedata["data"].(map[string]interface{})
	assert.True(t, ok)

	fileID, ok := filedata["id"].(string)
	assert.True(t, ok)

	res2, resbody := download(t, "/files/download/"+fileID, "")
	assert.Equal(t, 200, res2.StatusCode)
	assert.True(t, strings.HasPrefix(res2.Header.Get("Content-Disposition"), "inline"))
	assert.True(t, strings.Contains(res2.Header.Get("Content-Disposition"), `filename="downloadme1"`))
	assert.True(t, strings.HasPrefix(res2.Header.Get("Content-Type"), "text/plain"))
	assert.NotEmpty(t, res2.Header.Get("Etag"))
	assert.Equal(t, res2.Header.Get("Etag")[:1], `"`)
	assert.Equal(t, res2.Header.Get("Content-Length"), "3")
	assert.Equal(t, res2.Header.Get("Accept-Ranges"), "bytes")
	assert.Equal(t, body, string(resbody))
}

func TestDownloadFileByPathSuccess(t *testing.T) {
	body := "foo"
	res1, _ := upload(t, "/files/?Type=file&Name=downloadme2", "text/plain", body, "rL0Y20zC+Fzt72VPzMSk2A==")
	assert.Equal(t, 201, res1.StatusCode)

	res2, resbody := download(t, "/files/download?Dl=1&Path="+url.QueryEscape("/downloadme2"), "")
	assert.Equal(t, 200, res2.StatusCode)
	assert.True(t, strings.HasPrefix(res2.Header.Get("Content-Disposition"), "attachment"))
	assert.True(t, strings.Contains(res2.Header.Get("Content-Disposition"), `filename="downloadme2"`))
	assert.True(t, strings.HasPrefix(res2.Header.Get("Content-Type"), "text/plain"))
	assert.Equal(t, res2.Header.Get("Content-Length"), "3")
	assert.Equal(t, res2.Header.Get("Accept-Ranges"), "bytes")
	assert.Equal(t, body, string(resbody))
}

func TestDownloadRangeSuccess(t *testing.T) {
	body := "foo,bar"
	res1, _ := upload(t, "/files/?Type=file&Name=downloadmebyrange", "text/plain", body, "UmfjCVWct/albVkURcJJfg==")
	assert.Equal(t, 201, res1.StatusCode)

	res2, _ := download(t, "/files/download?Path="+url.QueryEscape("/downloadmebyrange"), "nimp")
	assert.Equal(t, 416, res2.StatusCode)

	res3, res3body := download(t, "/files/download?Path="+url.QueryEscape("/downloadmebyrange"), "bytes=0-2")
	assert.Equal(t, 206, res3.StatusCode)
	assert.Equal(t, "foo", string(res3body))

	res4, res4body := download(t, "/files/download?Path="+url.QueryEscape("/downloadmebyrange"), "bytes=4-")
	assert.Equal(t, 206, res4.StatusCode)
	assert.Equal(t, "bar", string(res4body))
}

func TestGetFileMetadataFromPath(t *testing.T) {
	res1, _ := httpGet(ts.URL + "/files/metadata?Path=/noooooop")
	assert.Equal(t, 404, res1.StatusCode)

	body := "foo,bar"
	res2, _ := upload(t, "/files/?Type=file&Name=getmetadata", "text/plain", body, "UmfjCVWct/albVkURcJJfg==")
	assert.Equal(t, 201, res2.StatusCode)

	res3, _ := httpGet(ts.URL + "/files/metadata?Path=/getmetadata")
	assert.Equal(t, 200, res3.StatusCode)
}

func TestGetDirMetadataFromPath(t *testing.T) {
	res1, _ := createDir(t, "/files/?Name=getdirmeta&Type=directory")
	assert.Equal(t, 201, res1.StatusCode)

	res2, _ := httpGet(ts.URL + "/files/metadata?Path=/getdirmeta")
	assert.Equal(t, 200, res2.StatusCode)
}

func TestGetFileMetadataFromID(t *testing.T) {
	res1, _ := httpGet(ts.URL + "/files/qsdqsd")
	assert.Equal(t, 404, res1.StatusCode)

	body := "foo,bar"
	res2, data2 := upload(t, "/files/?Type=file&Name=getmetadatafromid", "text/plain", body, "UmfjCVWct/albVkURcJJfg==")
	assert.Equal(t, 201, res2.StatusCode)

	fileID, _ := extractDirData(t, data2)

	res3, _ := httpGet(ts.URL + "/files/" + fileID)
	assert.Equal(t, 200, res3.StatusCode)
}

func TestGetDirMetadataFromID(t *testing.T) {
	res1, data1 := createDir(t, "/files/?Name=getdirmetafromid&Type=directory")
	assert.Equal(t, 201, res1.StatusCode)

	parentID, _ := extractDirData(t, data1)

	body := "foo"
	res2, data2 := upload(t, "/files/"+parentID+"?Type=file&Name=firstfile", "text/plain", body, "rL0Y20zC+Fzt72VPzMSk2A==")
	assert.Equal(t, 201, res2.StatusCode)

	fileID, _ := extractDirData(t, data2)

	res3, _ := httpGet(ts.URL + "/files/" + fileID)
	assert.Equal(t, 200, res3.StatusCode)
}

func TestVersions(t *testing.T) {
	cfg := config.GetConfig()
	oldDelay := cfg.Fs.Versioning.MinDelayBetweenTwoVersions
	cfg.Fs.Versioning.MinDelayBetweenTwoVersions = 10 * time.Millisecond
	defer func() {
		cfg.Fs.Versioning.MinDelayBetweenTwoVersions = oldDelay
	}()

	res1, body1 := upload(t, "/files/?Type=file&Name=versioned", "text/plain", "one", "")
	assert.Equal(t, 201, res1.StatusCode)
	data1 := body1["data"].(map[string]interface{})
	attr1 := data1["attributes"].(map[string]interface{})
	sum1 := attr1["md5sum"]
	fileID := data1["id"].(string)
	time.Sleep(20 * time.Millisecond)
	res2, body2 := uploadMod(t, "/files/"+fileID, "text/plain", "two", "")
	assert.Equal(t, 200, res2.StatusCode)
	data2 := body2["data"].(map[string]interface{})
	attr2 := data2["attributes"].(map[string]interface{})
	sum2 := attr2["md5sum"]
	time.Sleep(20 * time.Millisecond)
	res3, body3 := uploadMod(t, "/files/"+fileID, "text/plain", "three", "")
	assert.Equal(t, 200, res3.StatusCode)
	data3 := body3["data"].(map[string]interface{})
	attr3 := data3["attributes"].(map[string]interface{})
	sum3 := attr3["md5sum"]

	res4, _ := httpGet(ts.URL + "/files/" + fileID)
	assert.Equal(t, 200, res4.StatusCode)
	var body map[string]interface{}
	assert.NoError(t, json.NewDecoder(res4.Body).Decode(&body))
	data := body["data"].(map[string]interface{})
	attr := data["attributes"].(map[string]interface{})
	assert.Equal(t, sum3, attr["md5sum"])

	rels := data["relationships"].(map[string]interface{})
	old := rels["old_versions"].(map[string]interface{})
	refs := old["data"].([]interface{})
	assert.Len(t, refs, 2)
	first := refs[0].(map[string]interface{})
	assert.Equal(t, consts.FilesVersions, first["type"])
	oneID := first["id"]
	second := refs[1].(map[string]interface{})
	assert.Equal(t, consts.FilesVersions, second["type"])
	twoID := second["id"]

	included := body["included"].([]interface{})
	vone := included[0].(map[string]interface{})
	assert.Equal(t, oneID, vone["id"])
	attrv1 := vone["attributes"].(map[string]interface{})
	assert.Equal(t, sum1, attrv1["md5sum"])
	vtwo := included[1].(map[string]interface{})
	assert.Equal(t, twoID, vtwo["id"])
	attrv2 := vtwo["attributes"].(map[string]interface{})
	assert.Equal(t, sum2, attrv2["md5sum"])
}

func TestPatchVersion(t *testing.T) {
	res1, body1 := upload(t, "/files/?Type=file&Name=patch-version", "text/plain", "one", "")
	assert.Equal(t, 201, res1.StatusCode)
	data1 := body1["data"].(map[string]interface{})
	fileID := data1["id"].(string)
	res2, _ := uploadMod(t, "/files/"+fileID, "text/plain", "two", "")
	assert.Equal(t, 200, res2.StatusCode)

	res3, _ := httpGet(ts.URL + "/files/" + fileID)
	assert.Equal(t, 200, res3.StatusCode)
	var body3 map[string]interface{}
	assert.NoError(t, json.NewDecoder(res3.Body).Decode(&body3))
	data3 := body3["data"].(map[string]interface{})
	rels := data3["relationships"].(map[string]interface{})
	old := rels["old_versions"].(map[string]interface{})
	refs := old["data"].([]interface{})
	assert.Len(t, refs, 1)
	ref := refs[0].(map[string]interface{})
	assert.Equal(t, consts.FilesVersions, ref["type"])
	versionID := ref["id"].(string)

	attrs := map[string]interface{}{
		"tags": []string{"qux"},
	}
	res4, body4 := patchFile(t, "/files/"+versionID, consts.FilesVersions, versionID, attrs, nil)
	assert.Equal(t, 200, res4.StatusCode)
	data4 := body4["data"].(map[string]interface{})
	assert.Equal(t, versionID, data4["id"])
	attrs4 := data4["attributes"].(map[string]interface{})
	tags := attrs4["tags"].([]interface{})
	assert.Len(t, tags, 1)
	assert.Equal(t, "qux", tags[0])
}

func TestDownloadVersion(t *testing.T) {
	content := "one"
	res1, body1 := upload(t, "/files/?Type=file&Name=downloadme-versioned", "text/plain", content, "")
	assert.Equal(t, 201, res1.StatusCode)
	data := body1["data"].(map[string]interface{})
	fileID := data["id"].(string)
	meta := data["meta"].(map[string]interface{})
	firstRev := meta["rev"].(string)

	res2, _ := uploadMod(t, "/files/"+fileID, "text/plain", "two", "")
	assert.Equal(t, 200, res2.StatusCode)

	res3, resbody := download(t, "/files/download/"+fileID+"/"+firstRev, "")
	assert.Equal(t, 200, res3.StatusCode)
	assert.True(t, strings.HasPrefix(res3.Header.Get("Content-Disposition"), "inline"))
	assert.True(t, strings.Contains(res3.Header.Get("Content-Disposition"), `filename="downloadme-versioned"`))
	assert.True(t, strings.HasPrefix(res3.Header.Get("Content-Type"), "text/plain"))
	assert.Equal(t, content, string(resbody))
}

func TestFileCreateAndDownloadByVersionID(t *testing.T) {
	content := "one"
	res1, body1 := upload(t, "/files/?Type=file&Name=direct-downloadme-versioned", "text/plain", content, "")
	assert.Equal(t, 201, res1.StatusCode)
	data := body1["data"].(map[string]interface{})
	fileID := data["id"].(string)
	meta := data["meta"].(map[string]interface{})
	firstRev := meta["rev"].(string)

	res2, _ := uploadMod(t, "/files/"+fileID, "text/plain", "two", "")
	assert.Equal(t, 200, res2.StatusCode)

	req, err := http.NewRequest("POST", ts.URL+"/files/downloads?VersionId="+fileID+"/"+firstRev, nil)
	assert.NoError(t, err)
	req.Header.Add(echo.HeaderAuthorization, "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	assert.Equal(t, 200, res.StatusCode)
	var data2 map[string]interface{}
	err = json.NewDecoder(res.Body).Decode(&data2)
	assert.NoError(t, err)

	displayURL := ts.URL + data2["links"].(map[string]interface{})["related"].(string)
	res3, err := http.Get(displayURL)
	assert.NoError(t, err)
	assert.Equal(t, 200, res3.StatusCode)
	disposition := res3.Header.Get("Content-Disposition")
	assert.Equal(t, `inline; filename="direct-downloadme-versioned"`, disposition)
}

func TestRevertVersion(t *testing.T) {
	content := "one"
	res1, body1 := upload(t, "/files/?Type=file&Name=downloadme-reverted", "text/plain", content, "")
	assert.Equal(t, 201, res1.StatusCode)
	data := body1["data"].(map[string]interface{})
	fileID := data["id"].(string)

	res2, _ := uploadMod(t, "/files/"+fileID, "text/plain", "two", "")
	assert.Equal(t, 200, res2.StatusCode)

	res3, _ := httpGet(ts.URL + "/files/" + fileID)
	assert.Equal(t, 200, res3.StatusCode)
	var body map[string]interface{}
	assert.NoError(t, json.NewDecoder(res3.Body).Decode(&body))
	data = body["data"].(map[string]interface{})
	rels := data["relationships"].(map[string]interface{})
	old := rels["old_versions"].(map[string]interface{})
	refs := old["data"].([]interface{})
	assert.Len(t, refs, 1)
	version := refs[0].(map[string]interface{})
	versionID := version["id"].(string)

	req4, _ := http.NewRequest("POST", ts.URL+"/files/revert/"+versionID, strings.NewReader(""))
	req4.Header.Add(echo.HeaderAuthorization, "Bearer "+token)
	res4, err := http.DefaultClient.Do(req4)
	assert.NoError(t, err)
	assert.Equal(t, 200, res4.StatusCode)

	res5, resbody := download(t, "/files/download/"+fileID, "")
	assert.Equal(t, 200, res5.StatusCode)
	assert.True(t, strings.HasPrefix(res5.Header.Get("Content-Disposition"), "inline"))
	assert.True(t, strings.Contains(res5.Header.Get("Content-Disposition"), `filename="downloadme-reverted"`))
	assert.True(t, strings.HasPrefix(res5.Header.Get("Content-Type"), "text/plain"))
	assert.Equal(t, content, string(resbody))
}

func TestCleanOldVersion(t *testing.T) {
	content := "one"
	res1, body1 := upload(t, "/files/?Type=file&Name=downloadme-toclean", "text/plain", content, "")
	assert.Equal(t, 201, res1.StatusCode)
	data := body1["data"].(map[string]interface{})
	fileID := data["id"].(string)

	res2, _ := uploadMod(t, "/files/"+fileID, "text/plain", "two", "")
	assert.Equal(t, 200, res2.StatusCode)

	res3, _ := httpGet(ts.URL + "/files/" + fileID)
	assert.Equal(t, 200, res3.StatusCode)
	var body map[string]interface{}
	assert.NoError(t, json.NewDecoder(res3.Body).Decode(&body))
	data = body["data"].(map[string]interface{})
	rels := data["relationships"].(map[string]interface{})
	old := rels["old_versions"].(map[string]interface{})
	refs := old["data"].([]interface{})
	assert.Len(t, refs, 1)
	version := refs[0].(map[string]interface{})
	versionID := version["id"].(string)

	req4, _ := http.NewRequest("DELETE", ts.URL+"/files/"+versionID, nil)
	req4.Header.Add(echo.HeaderAuthorization, "Bearer "+token)
	res4, err := http.DefaultClient.Do(req4)
	assert.NoError(t, err)
	assert.Equal(t, 204, res4.StatusCode)

	res5, _ := download(t, "/files/download/"+versionID, "")
	assert.Equal(t, 404, res5.StatusCode)
}

func TestCopyVersion(t *testing.T) {
	buf := strings.NewReader(`{
    "data": {
        "type": "io.cozy.files.metadata",
        "attributes": {
            "category": "report",
            "label": "foo"
        }
    }
}`)
	req1, err := http.NewRequest("POST", ts.URL+"/files/upload/metadata", buf)
	req1.Header.Add(echo.HeaderAuthorization, "Bearer "+token)
	assert.NoError(t, err)
	res1, err := http.DefaultClient.Do(req1)
	assert.NoError(t, err)
	defer res1.Body.Close()
	assert.Equal(t, 201, res1.StatusCode)
	var obj1 map[string]interface{}
	err = extractJSONRes(res1, &obj1)
	assert.NoError(t, err)
	data1 := obj1["data"].(map[string]interface{})
	secret := data1["id"].(string)

	content := "should-be-the-same-after-copy"
	u := "/files/?Type=file&Name=version-to-be-copied&MetadataID=" + secret
	res2, body2 := upload(t, u, "text/plain", content, "")
	assert.Equal(t, 201, res2.StatusCode)
	data2 := body2["data"].(map[string]interface{})
	fileID := data2["id"].(string)
	attrs2 := data2["attributes"].(map[string]interface{})
	meta2 := attrs2["metadata"].(map[string]interface{})
	assert.Equal(t, "report", meta2["category"])
	assert.Equal(t, "foo", meta2["label"])

	buf = strings.NewReader(`{
    "data": {
        "type": "io.cozy.files.metadata",
        "attributes": {
            "label": "bar"
        }
    }
}`)
	req3, err := http.NewRequest("POST", ts.URL+"/files/"+fileID+"/versions?Tags=qux", buf)
	req3.Header.Add(echo.HeaderAuthorization, "Bearer "+token)
	assert.NoError(t, err)
	res3, err := http.DefaultClient.Do(req3)
	assert.NoError(t, err)
	defer res3.Body.Close()
	assert.Equal(t, 200, res3.StatusCode)
	var obj3 map[string]interface{}
	err = extractJSONRes(res3, &obj3)
	assert.NoError(t, err)
	data3 := obj3["data"].(map[string]interface{})
	attrs3 := data3["attributes"].(map[string]interface{})
	meta3 := attrs3["metadata"].(map[string]interface{})
	assert.Nil(t, meta3["category"])
	assert.Equal(t, "bar", meta3["label"])
	assert.Len(t, attrs3["tags"], 1)
	tags := attrs3["tags"].([]interface{})
	assert.Equal(t, "qux", tags[0])
}

func TestCopyVersionWithCertified(t *testing.T) {
	buf := strings.NewReader(`{
    "data": {
        "type": "io.cozy.files.metadata",
        "attributes": {
            "carbonCopy": true,
            "electronicSafe": true
        }
    }
}`)
	req1, err := http.NewRequest("POST", ts.URL+"/files/upload/metadata", buf)
	req1.Header.Add(echo.HeaderAuthorization, "Bearer "+token)
	assert.NoError(t, err)
	res1, err := http.DefaultClient.Do(req1)
	assert.NoError(t, err)
	defer res1.Body.Close()
	assert.Equal(t, 201, res1.StatusCode)
	var obj1 map[string]interface{}
	err = extractJSONRes(res1, &obj1)
	assert.NoError(t, err)
	data1 := obj1["data"].(map[string]interface{})
	secret := data1["id"].(string)

	content := "certified carbonCopy and electronicSafe must be kept if only the qualification change"
	u := "/files/?Type=file&Name=copy-version-with-certified&MetadataID=" + secret
	res2, body2 := upload(t, u, "text/plain", content, "")
	assert.Equal(t, 201, res2.StatusCode)
	data2 := body2["data"].(map[string]interface{})
	fileID := data2["id"].(string)
	attrs2 := data2["attributes"].(map[string]interface{})
	meta2 := attrs2["metadata"].(map[string]interface{})
	assert.NotNil(t, meta2["carbonCopy"])
	assert.NotNil(t, meta2["electronicSafe"])

	buf = strings.NewReader(`{
    "data": {
        "type": "io.cozy.files.metadata",
        "attributes": {
			"qualification": { "purpose": "attestation" }
        }
    }
}`)
	req3, err := http.NewRequest("POST", ts.URL+"/files/"+fileID+"/versions", buf)
	req3.Header.Add(echo.HeaderAuthorization, "Bearer "+token)
	assert.NoError(t, err)
	res3, err := http.DefaultClient.Do(req3)
	assert.NoError(t, err)
	defer res3.Body.Close()
	assert.Equal(t, 200, res3.StatusCode)
	var obj3 map[string]interface{}
	err = extractJSONRes(res3, &obj3)
	assert.NoError(t, err)
	data3 := obj3["data"].(map[string]interface{})
	attrs3 := data3["attributes"].(map[string]interface{})
	meta3 := attrs3["metadata"].(map[string]interface{})
	assert.NotNil(t, meta3["qualification"])
	assert.NotNil(t, meta3["carbonCopy"])
	assert.NotNil(t, meta3["electronicSafe"])

	buf = strings.NewReader(`{
    "data": {
        "type": "io.cozy.files.metadata",
        "attributes": {
            "label": "bar"
        }
    }
}`)
	req4, err := http.NewRequest("POST", ts.URL+"/files/"+fileID+"/versions", buf)
	req4.Header.Add(echo.HeaderAuthorization, "Bearer "+token)
	assert.NoError(t, err)
	res4, err := http.DefaultClient.Do(req4)
	assert.NoError(t, err)
	defer res4.Body.Close()
	assert.Equal(t, 200, res4.StatusCode)
	var obj4 map[string]interface{}
	err = extractJSONRes(res4, &obj4)
	assert.NoError(t, err)
	data4 := obj4["data"].(map[string]interface{})
	attrs4 := data4["attributes"].(map[string]interface{})
	meta4 := attrs4["metadata"].(map[string]interface{})
	assert.NotNil(t, meta4["label"])
	assert.Nil(t, meta4["qualification"])
	assert.Nil(t, meta4["carbonCopy"])
	assert.Nil(t, meta4["electronicSafe"])
}

func TestCopyVersionWorksForNotes(t *testing.T) {
	content := "# Title\n\n* foo\n* bar\n"
	u := "/files/?Type=file&Name=test.cozy-note"
	res, body := upload(t, u, consts.NoteMimeType, content, "")
	assert.Equal(t, 201, res.StatusCode)
	data := body["data"].(map[string]interface{})
	fileID := data["id"].(string)
	attrs := data["attributes"].(map[string]interface{})
	meta := attrs["metadata"].(map[string]interface{})
	assert.NotNil(t, meta["title"])
	assert.NotNil(t, meta["content"])
	assert.NotNil(t, meta["schema"])
	assert.NotNil(t, meta["version"])

	buf := strings.NewReader(`{
    "data": {
        "type": "io.cozy.files.metadata",
        "attributes": {
			"qualification": { "purpose": "attestation" }
        }
    }
}`)
	req2, err := http.NewRequest("POST", ts.URL+"/files/"+fileID+"/versions", buf)
	req2.Header.Add(echo.HeaderAuthorization, "Bearer "+token)
	assert.NoError(t, err)
	res2, err := http.DefaultClient.Do(req2)
	assert.NoError(t, err)
	defer res2.Body.Close()
	assert.Equal(t, 200, res2.StatusCode)
	var obj2 map[string]interface{}
	err = extractJSONRes(res2, &obj2)
	assert.NoError(t, err)
	data2 := obj2["data"].(map[string]interface{})
	attrs2 := data2["attributes"].(map[string]interface{})
	meta2 := attrs2["metadata"].(map[string]interface{})
	assert.NotNil(t, meta2["qualification"])
	assert.Equal(t, meta["title"], meta2["title"])
	assert.Equal(t, meta["content"], meta2["content"])
	assert.Equal(t, meta["schema"], meta2["schema"])
	assert.Equal(t, meta["version"], meta2["version"])
}

func TestArchiveNoFiles(t *testing.T) {
	body := bytes.NewBufferString(`{
		"data": {
			"attributes": {}
		}
	}`)
	req, err := http.NewRequest("POST", ts.URL+"/files/archive", body)
	if !assert.NoError(t, err) {
		return
	}
	req.Header.Add("Content-Type", "application/vnd.api+json")
	req.Header.Add(echo.HeaderAuthorization, "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if !assert.NoError(t, err) {
		return
	}

	assert.NoError(t, err)
	assert.Equal(t, 400, res.StatusCode)
	msg, err := ioutil.ReadAll(res.Body)
	assert.NoError(t, err)
	actual := strings.TrimSpace(string(msg))
	assert.Equal(t, `"Can't create an archive with no files"`, actual)
}

func TestArchiveDirectDownload(t *testing.T) {
	res1, data1 := createDir(t, "/files/?Name=archive&Type=directory")
	if !assert.Equal(t, 201, res1.StatusCode) {
		return
	}
	dirID, _ := extractDirData(t, data1)
	names := []string{"foo", "bar", "baz"}
	for _, name := range names {
		res2, _ := createDir(t, "/files/"+dirID+"?Name="+name+".jpg&Type=file")
		if !assert.Equal(t, 201, res2.StatusCode) {
			return
		}
	}

	// direct download
	body := bytes.NewBufferString(`{
		"data": {
			"attributes": {
				"files": [
					"/archive/foo.jpg",
					"/archive/bar.jpg",
					"/archive/baz.jpg"
				]
			}
		}
	}`)

	req, err := http.NewRequest("POST", ts.URL+"/files/archive", body)
	req.Header.Add(echo.HeaderAuthorization, "Bearer "+token)
	req.Header.Add("Content-Type", "application/zip")
	req.Header.Add("Accept", "application/zip")
	assert.NoError(t, err)
	res, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	assert.Equal(t, 200, res.StatusCode)
	assert.Equal(t, "application/zip", res.Header.Get("Content-Type"))
}

func TestArchiveCreateAndDownload(t *testing.T) {
	res1, data1 := createDir(t, "/files/?Name=archive2&Type=directory")
	if !assert.Equal(t, 201, res1.StatusCode) {
		return
	}
	dirID, _ := extractDirData(t, data1)
	names := []string{"foo", "bar", "baz"}
	for _, name := range names {
		res2, _ := createDir(t, "/files/"+dirID+"?Name="+name+".jpg&Type=file")
		if !assert.Equal(t, 201, res2.StatusCode) {
			return
		}
	}

	body := bytes.NewBufferString(`{
		"data": {
			"attributes": {
				"files": [
					"/archive/foo.jpg",
					"/archive/bar.jpg",
					"/archive/baz.jpg"
				]
			}
		}
	}`)

	req, err := http.NewRequest("POST", ts.URL+"/files/archive", body)
	if !assert.NoError(t, err) {
		return
	}
	req.Header.Add("Content-Type", "application/vnd.api+json")
	req.Header.Add(echo.HeaderAuthorization, "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if !assert.NoError(t, err) {
		return
	}

	assert.NoError(t, err)
	assert.Equal(t, 200, res.StatusCode)
	var data map[string]interface{}
	err = json.NewDecoder(res.Body).Decode(&data)
	assert.NoError(t, err)

	downloadURL := ts.URL + data["links"].(map[string]interface{})["related"].(string)
	res2, err := httpGet(downloadURL)
	assert.NoError(t, err)
	assert.Equal(t, 200, res2.StatusCode)
	disposition := res2.Header.Get("Content-Disposition")
	assert.Equal(t, `attachment; filename="archive.zip"`, disposition)
}

func TestFileCreateAndDownloadByPath(t *testing.T) {
	body := "foo,bar"
	res1, _ := upload(t, "/files/?Type=file&Name=todownload2steps", "text/plain", body, "UmfjCVWct/albVkURcJJfg==")
	if !assert.Equal(t, 201, res1.StatusCode) {
		return
	}

	path := "/todownload2steps"

	req, err := http.NewRequest("POST", ts.URL+"/files/downloads?Path="+path, nil)
	if !assert.NoError(t, err) {
		return
	}
	req.Header.Add("Content-Type", "")
	req.Header.Add(echo.HeaderAuthorization, "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if !assert.NoError(t, err) {
		return
	}

	assert.NoError(t, err)
	assert.Equal(t, 200, res.StatusCode)
	var data map[string]interface{}
	err = json.NewDecoder(res.Body).Decode(&data)
	assert.NoError(t, err)

	displayURL := ts.URL + data["links"].(map[string]interface{})["related"].(string)
	res2, err := http.Get(displayURL)
	assert.NoError(t, err)
	assert.Equal(t, 200, res2.StatusCode)
	disposition := res2.Header.Get("Content-Disposition")
	assert.Equal(t, `inline; filename="todownload2steps"`, disposition)

	downloadURL := ts.URL + data["links"].(map[string]interface{})["related"].(string) + "?Dl=1"
	res3, err := http.Get(downloadURL)
	assert.NoError(t, err)
	assert.Equal(t, 200, res3.StatusCode)
	disposition = res3.Header.Get("Content-Disposition")
	assert.Equal(t, `attachment; filename="todownload2steps"`, disposition)
}

func TestFileCreateAndDownloadByID(t *testing.T) {
	body := "foo,bar"
	res1, v := upload(t, "/files/?Type=file&Name=todownload2stepsbis", "text/plain", body, "UmfjCVWct/albVkURcJJfg==")
	if !assert.Equal(t, 201, res1.StatusCode) {
		return
	}
	id := v["data"].(map[string]interface{})["id"].(string)

	req, err := http.NewRequest("POST", ts.URL+"/files/downloads?Id="+id, nil)
	if !assert.NoError(t, err) {
		return
	}
	req.Header.Add("Content-Type", "")
	req.Header.Add(echo.HeaderAuthorization, "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if !assert.NoError(t, err) {
		return
	}

	assert.NoError(t, err)
	assert.Equal(t, 200, res.StatusCode)
	var data map[string]interface{}
	err = json.NewDecoder(res.Body).Decode(&data)
	assert.NoError(t, err)

	displayURL := ts.URL + data["links"].(map[string]interface{})["related"].(string)
	res2, err := http.Get(displayURL)
	assert.NoError(t, err)
	assert.Equal(t, 200, res2.StatusCode)
	disposition := res2.Header.Get("Content-Disposition")
	assert.Equal(t, `inline; filename="todownload2stepsbis"`, disposition)
}

func TestEncryptedFileCreate(t *testing.T) {
	res1, data1 := upload(t, "/files/?Type=file&Name=encryptedfile&Encrypted=true", "text/plain", "foo", "")
	assert.Equal(t, 201, res1.StatusCode)

	var ok bool
	resData, ok := data1["data"].(map[string]interface{})
	assert.True(t, ok)

	attrs := resData["attributes"].(map[string]interface{})
	assert.Equal(t, attrs["name"].(string), "encryptedfile")
	assert.True(t, attrs["encrypted"].(bool))
}

func TestHeadDirOrFileNotFound(t *testing.T) {
	req, _ := http.NewRequest("HEAD", ts.URL+"/files/fakeid/?Type=directory", strings.NewReader(""))
	req.Header.Add(echo.HeaderAuthorization, "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	assert.Equal(t, 404, res.StatusCode)
}

func TestHeadDirOrFileExists(t *testing.T) {
	res, _ := createDir(t, "/files/?Name=hellothere&Type=directory")
	assert.Equal(t, 201, res.StatusCode)

	storage := testInstance.VFS()
	dir, err := storage.DirByPath("/hellothere")
	assert.NoError(t, err)
	id := dir.ID()
	req, _ := http.NewRequest("HEAD", ts.URL+"/files/"+id+"?Type=directory", strings.NewReader(""))
	req.Header.Add(echo.HeaderAuthorization, "Bearer "+token)
	res, err = http.DefaultClient.Do(req)
	assert.NoError(t, err)
	assert.Equal(t, 200, res.StatusCode)
}

func TestArchiveNotFound(t *testing.T) {
	body := bytes.NewBufferString(`{
		"data": {
			"attributes": {
				"files": [
					"/archive/foo.jpg",
					"/no/such/file",
					"/archive/baz.jpg"
				]
			}
		}
	}`)
	req, err := http.NewRequest("POST", ts.URL+"/files/archive", body)
	if !assert.NoError(t, err) {
		return
	}
	req.Header.Add("Content-Type", "application/vnd.api+json")
	req.Header.Add(echo.HeaderAuthorization, "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if !assert.NoError(t, err) {
		return
	}
	assert.Equal(t, 404, res.StatusCode)
}

func TestDirTrash(t *testing.T) {
	res1, data1 := createDir(t, "/files/?Name=totrashdir&Type=directory")
	if !assert.Equal(t, 201, res1.StatusCode) {
		return
	}

	dirID, _ := extractDirData(t, data1)

	res2, _ := createDir(t, "/files/"+dirID+"?Name=child1&Type=file")
	if !assert.Equal(t, 201, res2.StatusCode) {
		return
	}
	res3, _ := createDir(t, "/files/"+dirID+"?Name=child2&Type=file")
	if !assert.Equal(t, 201, res3.StatusCode) {
		return
	}

	res4, _ := trash(t, "/files/"+dirID)
	if !assert.Equal(t, 200, res4.StatusCode) {
		return
	}

	res5, err := httpGet(ts.URL + "/files/" + dirID)
	if !assert.NoError(t, err) || !assert.Equal(t, 200, res5.StatusCode) {
		return
	}

	res6, err := httpGet(ts.URL + "/files/download?Path=" + url.QueryEscape(vfs.TrashDirName+"/totrashdir/child1"))
	if !assert.NoError(t, err) || !assert.Equal(t, 200, res6.StatusCode) {
		return
	}

	res7, err := httpGet(ts.URL + "/files/download?Path=" + url.QueryEscape(vfs.TrashDirName+"/totrashdir/child2"))
	if !assert.NoError(t, err) || !assert.Equal(t, 200, res7.StatusCode) {
		return
	}

	res8, _ := trash(t, "/files/"+dirID)
	if !assert.Equal(t, 400, res8.StatusCode) {
		return
	}
}

func TestFileTrash(t *testing.T) {
	body := "foo,bar"
	res1, data1 := upload(t, "/files/?Type=file&Name=totrashfile", "text/plain", body, "UmfjCVWct/albVkURcJJfg==")
	if !assert.Equal(t, 201, res1.StatusCode) {
		return
	}

	fileID, _ := extractDirData(t, data1)

	res2, _ := trash(t, "/files/"+fileID)
	if !assert.Equal(t, 200, res2.StatusCode) {
		return
	}

	res3, err := httpGet(ts.URL + "/files/download?Path=" + url.QueryEscape(vfs.TrashDirName+"/totrashfile"))
	if !assert.NoError(t, err) || !assert.Equal(t, 200, res3.StatusCode) {
		return
	}

	res4, _ := trash(t, "/files/"+fileID)
	if !assert.Equal(t, 400, res4.StatusCode) {
		return
	}

	res5, data2 := upload(t, "/files/?Type=file&Name=totrashfile2", "text/plain", body, "UmfjCVWct/albVkURcJJfg==")
	if !assert.Equal(t, 201, res5.StatusCode) {
		return
	}

	fileID, v2 := extractDirData(t, data2)
	meta2 := v2["meta"].(map[string]interface{})
	rev2 := meta2["rev"].(string)

	req6, err := http.NewRequest("DELETE", ts.URL+"/files/"+fileID, nil)
	req6.Header.Add(echo.HeaderAuthorization, "Bearer "+token)
	assert.NoError(t, err)

	req6.Header.Add("If-Match", "badrev")
	res6, err := http.DefaultClient.Do(req6)
	assert.NoError(t, err)
	assert.Equal(t, 412, res6.StatusCode)

	res7, err := httpGet(ts.URL + "/files/download?Path=" + url.QueryEscape(vfs.TrashDirName+"/totrashfile"))
	if !assert.NoError(t, err) || !assert.Equal(t, 200, res7.StatusCode) {
		return
	}

	req8, err := http.NewRequest("DELETE", ts.URL+"/files/"+fileID, nil)
	req8.Header.Add(echo.HeaderAuthorization, "Bearer "+token)
	assert.NoError(t, err)

	req8.Header.Add("If-Match", rev2)
	res8, err := http.DefaultClient.Do(req8)
	assert.NoError(t, err)
	assert.Equal(t, 200, res8.StatusCode)

	res9, _ := trash(t, "/files/"+fileID)
	if !assert.Equal(t, 400, res9.StatusCode) {
		return
	}
}

func TestForbidMovingTrashedFile(t *testing.T) {
	body := "foo,bar"
	res1, data1 := upload(t, "/files/?Type=file&Name=forbidmovingtrashedfile", "text/plain", body, "UmfjCVWct/albVkURcJJfg==")
	if !assert.Equal(t, 201, res1.StatusCode) {
		return
	}

	fileID, _ := extractDirData(t, data1)
	res2, _ := trash(t, "/files/"+fileID)
	if !assert.Equal(t, 200, res2.StatusCode) {
		return
	}

	attrs := map[string]interface{}{
		"dir_id": consts.RootDirID,
	}
	res3, _ := patchFile(t, "/files/"+fileID, "file", fileID, attrs, nil)
	assert.Equal(t, 400, res3.StatusCode)
}

func TestFileRestore(t *testing.T) {
	body := "foo,bar"
	res1, data1 := upload(t, "/files/?Type=file&Name=torestorefile", "text/plain", body, "UmfjCVWct/albVkURcJJfg==")
	if !assert.Equal(t, 201, res1.StatusCode) {
		return
	}

	fileID, _ := extractDirData(t, data1)

	res2, body2 := trash(t, "/files/"+fileID)
	if !assert.Equal(t, 200, res2.StatusCode) {
		return
	}
	data2 := body2["data"].(map[string]interface{})
	attrs2 := data2["attributes"].(map[string]interface{})
	trashed := attrs2["trashed"].(bool)
	assert.True(t, trashed)

	res3, body3 := restore(t, "/files/trash/"+fileID)
	if !assert.Equal(t, 200, res3.StatusCode) {
		return
	}
	data3 := body3["data"].(map[string]interface{})
	attrs3 := data3["attributes"].(map[string]interface{})
	trashed = attrs3["trashed"].(bool)
	assert.False(t, trashed)

	res4, err := httpGet(ts.URL + "/files/download?Path=" + url.QueryEscape("/torestorefile"))
	if !assert.NoError(t, err) || !assert.Equal(t, 200, res4.StatusCode) {
		return
	}
}

func TestFileRestoreWithConflicts(t *testing.T) {
	body := "foo,bar"
	res1, data1 := upload(t, "/files/?Type=file&Name=torestorefilewithconflict", "text/plain", body, "UmfjCVWct/albVkURcJJfg==")
	if !assert.Equal(t, 201, res1.StatusCode) {
		return
	}

	fileID, _ := extractDirData(t, data1)

	res2, _ := trash(t, "/files/"+fileID)
	if !assert.Equal(t, 200, res2.StatusCode) {
		return
	}

	res1, _ = upload(t, "/files/?Type=file&Name=torestorefilewithconflict", "text/plain", body, "UmfjCVWct/albVkURcJJfg==")
	if !assert.Equal(t, 201, res1.StatusCode) {
		return
	}

	res3, data3 := restore(t, "/files/trash/"+fileID)
	if !assert.Equal(t, 200, res3.StatusCode) {
		return
	}

	restoredID, restoredData := extractDirData(t, data3)
	if !assert.Equal(t, fileID, restoredID) {
		return
	}
	restoredData = restoredData["attributes"].(map[string]interface{})
	assert.True(t, strings.HasPrefix(restoredData["name"].(string), "torestorefilewithconflict"))
	assert.NotEqual(t, "torestorefilewithconflict", restoredData["name"].(string))
}

func TestFileRestoreWithWithoutParent(t *testing.T) {
	res1, data1 := createDir(t, "/files/?Type=directory&Name=torestorein")
	if !assert.Equal(t, 201, res1.StatusCode) {
		return
	}

	dirID, _ := extractDirData(t, data1)

	body := "foo,bar"
	res1, data1 = upload(t, "/files/"+dirID+"?Type=file&Name=torestorefilewithconflict", "text/plain", body, "UmfjCVWct/albVkURcJJfg==")
	if !assert.Equal(t, 201, res1.StatusCode) {
		return
	}

	fileID, _ := extractDirData(t, data1)

	res2, _ := trash(t, "/files/"+fileID)
	if !assert.Equal(t, 200, res2.StatusCode) {
		return
	}

	res2, _ = trash(t, "/files/"+dirID)
	if !assert.Equal(t, 200, res2.StatusCode) {
		return
	}

	res3, data3 := restore(t, "/files/trash/"+fileID)
	if !assert.Equal(t, 200, res3.StatusCode) {
		return
	}

	restoredID, restoredData := extractDirData(t, data3)
	if !assert.Equal(t, fileID, restoredID) {
		return
	}
	restoredData = restoredData["attributes"].(map[string]interface{})
	assert.Equal(t, "torestorefilewithconflict", restoredData["name"].(string))
	assert.NotEqual(t, consts.RootDirID, restoredData["dir_id"].(string))
}

func TestFileRestoreWithWithoutParent2(t *testing.T) {
	res1, data1 := createDir(t, "/files/?Type=directory&Name=torestorein2")
	if !assert.Equal(t, 201, res1.StatusCode) {
		return
	}

	dirID, _ := extractDirData(t, data1)

	body := "foo,bar"
	res1, data1 = upload(t, "/files/"+dirID+"?Type=file&Name=torestorefilewithconflict2", "text/plain", body, "UmfjCVWct/albVkURcJJfg==")
	if !assert.Equal(t, 201, res1.StatusCode) {
		return
	}

	fileID, _ := extractDirData(t, data1)

	res2, _ := trash(t, "/files/"+dirID)
	if !assert.Equal(t, 200, res2.StatusCode) {
		return
	}

	res3, data3 := restore(t, "/files/trash/"+fileID)
	if !assert.Equal(t, 200, res3.StatusCode) {
		return
	}

	restoredID, restoredData := extractDirData(t, data3)
	if !assert.Equal(t, fileID, restoredID) {
		return
	}
	restoredData = restoredData["attributes"].(map[string]interface{})
	assert.Equal(t, "torestorefilewithconflict2", restoredData["name"].(string))
	assert.NotEqual(t, consts.RootDirID, restoredData["dir_id"].(string))
}

func TestDirRestore(t *testing.T) {
	res1, data1 := createDir(t, "/files/?Type=directory&Name=torestoredir")
	if !assert.Equal(t, 201, res1.StatusCode) {
		return
	}

	dirID, _ := extractDirData(t, data1)

	body := "foo,bar"
	res2, data2 := upload(t, "/files/"+dirID+"?Type=file&Name=totrashfile", "text/plain", body, "UmfjCVWct/albVkURcJJfg==")
	if !assert.Equal(t, 201, res2.StatusCode) {
		return
	}

	fileID, _ := extractDirData(t, data2)

	res3, _ := trash(t, "/files/"+dirID)
	if !assert.Equal(t, 200, res3.StatusCode) {
		return
	}

	res4, err := httpGet(ts.URL + "/files/" + fileID)
	if !assert.NoError(t, err) || !assert.Equal(t, 200, res4.StatusCode) {
		return
	}

	var v map[string]interface{}
	err = extractJSONRes(res4, &v)
	assert.NoError(t, err)
	data := v["data"].(map[string]interface{})
	attrs := data["attributes"].(map[string]interface{})
	trashed := attrs["trashed"].(bool)
	assert.True(t, trashed)

	res5, _ := restore(t, "/files/trash/"+dirID)
	if !assert.Equal(t, 200, res5.StatusCode) {
		return
	}

	res6, err := httpGet(ts.URL + "/files/" + fileID)
	if !assert.NoError(t, err) || !assert.Equal(t, 200, res6.StatusCode) {
		return
	}

	err = extractJSONRes(res6, &v)
	assert.NoError(t, err)
	data = v["data"].(map[string]interface{})
	attrs = data["attributes"].(map[string]interface{})
	trashed = attrs["trashed"].(bool)
	assert.False(t, trashed)
}

func TestDirRestoreWithConflicts(t *testing.T) {
	res1, data1 := createDir(t, "/files/?Type=directory&Name=torestoredirwithconflict")
	if !assert.Equal(t, 201, res1.StatusCode) {
		return
	}

	dirID, _ := extractDirData(t, data1)

	res2, _ := trash(t, "/files/"+dirID)
	if !assert.Equal(t, 200, res2.StatusCode) {
		return
	}

	res1, _ = createDir(t, "/files/?Type=directory&Name=torestoredirwithconflict")
	if !assert.Equal(t, 201, res1.StatusCode) {
		return
	}

	res3, data3 := restore(t, "/files/trash/"+dirID)
	if !assert.Equal(t, 200, res3.StatusCode) {
		return
	}

	restoredID, restoredData := extractDirData(t, data3)
	if !assert.Equal(t, dirID, restoredID) {
		return
	}
	restoredData = restoredData["attributes"].(map[string]interface{})
	assert.True(t, strings.HasPrefix(restoredData["name"].(string), "torestoredirwithconflict"))
	assert.NotEqual(t, "torestoredirwithconflict", restoredData["name"].(string))
}

func TestTrashList(t *testing.T) {
	body := "foo,bar"
	res1, data1 := upload(t, "/files/?Type=file&Name=tolistfile", "text/plain", body, "UmfjCVWct/albVkURcJJfg==")
	if !assert.Equal(t, 201, res1.StatusCode) {
		return
	}

	res2, data2 := createDir(t, "/files/?Name=tolistdir&Type=directory")
	if !assert.Equal(t, 201, res2.StatusCode) {
		return
	}

	dirID, _ := extractDirData(t, data1)
	fileID, _ := extractDirData(t, data2)

	res3, _ := trash(t, "/files/"+dirID)
	if !assert.Equal(t, 200, res3.StatusCode) {
		return
	}

	res4, _ := trash(t, "/files/"+fileID)
	if !assert.Equal(t, 200, res4.StatusCode) {
		return
	}

	res5, err := httpGet(ts.URL + "/files/trash")
	if !assert.NoError(t, err) {
		return
	}
	defer res5.Body.Close()

	var v struct {
		Data []interface{} `json:"data"`
	}

	err = json.NewDecoder(res5.Body).Decode(&v)
	if !assert.NoError(t, err) {
		return
	}

	assert.True(t, len(v.Data) >= 2, "response should contains at least 2 items")
}

func TestTrashClear(t *testing.T) {
	body := "foo,bar"
	res1, data1 := upload(t, "/files/?Type=file&Name=tolistfile", "text/plain", body, "UmfjCVWct/albVkURcJJfg==")
	if !assert.Equal(t, 201, res1.StatusCode) {
		return
	}

	res2, data2 := createDir(t, "/files/?Name=tolistdir&Type=directory")
	if !assert.Equal(t, 201, res2.StatusCode) {
		return
	}

	dirID, _ := extractDirData(t, data1)
	fileID, _ := extractDirData(t, data2)

	res3, _ := trash(t, "/files/"+dirID)
	if !assert.Equal(t, 200, res3.StatusCode) {
		return
	}

	res4, _ := trash(t, "/files/"+fileID)
	if !assert.Equal(t, 200, res4.StatusCode) {
		return
	}

	path := "/files/trash"
	req, err := http.NewRequest(http.MethodDelete, ts.URL+path, nil)
	req.Header.Add(echo.HeaderAuthorization, "Bearer "+token)
	if !assert.NoError(t, err) {
		return
	}

	_, err = http.DefaultClient.Do(req)
	if !assert.NoError(t, err) {
		return
	}

	res5, err := httpGet(ts.URL + "/files/trash")
	if !assert.NoError(t, err) {
		return
	}
	defer res5.Body.Close()

	var v struct {
		Data []interface{} `json:"data"`
	}

	err = json.NewDecoder(res5.Body).Decode(&v)
	if !assert.NoError(t, err) {
		return
	}

	assert.True(t, len(v.Data) == 0)
}

func TestDestroyFile(t *testing.T) {
	body := "foo,bar"
	res1, data1 := upload(t, "/files/?Type=file&Name=tolistfile", "text/plain", body, "UmfjCVWct/albVkURcJJfg==")
	if !assert.Equal(t, 201, res1.StatusCode) {
		return
	}

	res2, data2 := createDir(t, "/files/?Name=tolistdir&Type=directory")
	if !assert.Equal(t, 201, res2.StatusCode) {
		return
	}

	dirID, _ := extractDirData(t, data1)
	fileID, _ := extractDirData(t, data2)

	res3, _ := trash(t, "/files/"+dirID)
	if !assert.Equal(t, 200, res3.StatusCode) {
		return
	}

	res4, _ := trash(t, "/files/"+fileID)
	if !assert.Equal(t, 200, res4.StatusCode) {
		return
	}

	path := "/files/trash/" + fileID
	req, err := http.NewRequest(http.MethodDelete, ts.URL+path, nil)
	req.Header.Add(echo.HeaderAuthorization, "Bearer "+token)
	if !assert.NoError(t, err) {
		return
	}

	_, err = http.DefaultClient.Do(req)
	if !assert.NoError(t, err) {
		return
	}

	res5, err := httpGet(ts.URL + "/files/trash")
	if !assert.NoError(t, err) {
		return
	}
	defer res5.Body.Close()

	var v struct {
		Data []interface{} `json:"data"`
	}

	err = json.NewDecoder(res5.Body).Decode(&v)
	if !assert.NoError(t, err) {
		return
	}
	assert.True(t, len(v.Data) == 1)

	path = "/files/trash/" + dirID
	req, err = http.NewRequest(http.MethodDelete, ts.URL+path, nil)
	req.Header.Add(echo.HeaderAuthorization, "Bearer "+token)
	if !assert.NoError(t, err) {
		return
	}

	_, err = http.DefaultClient.Do(req)
	if !assert.NoError(t, err) {
		return
	}

	res5, err = httpGet(ts.URL + "/files/trash")
	if !assert.NoError(t, err) {
		return
	}
	defer res5.Body.Close()

	err = json.NewDecoder(res5.Body).Decode(&v)
	if !assert.NoError(t, err) {
		return
	}
	assert.True(t, len(v.Data) == 0)
}

func TestThumbnailImages(t *testing.T) {
	res1, _ := httpGet(ts.URL + "/files/" + imgID)
	assert.Equal(t, 200, res1.StatusCode)
	var obj map[string]interface{}
	err := extractJSONRes(res1, &obj)
	assert.NoError(t, err)
	data := obj["data"].(map[string]interface{})
	links := data["links"].(map[string]interface{})
	large := links["large"].(string)
	medium := links["medium"].(string)
	small := links["small"].(string)
	tiny := links["tiny"].(string)

	res2, _ := download(t, large, "")
	assert.Equal(t, 200, res2.StatusCode)
	assert.True(t, strings.HasPrefix(res2.Header.Get("Content-Type"), "image/jpeg"))
	res3, _ := download(t, medium, "")
	assert.Equal(t, 200, res3.StatusCode)
	assert.True(t, strings.HasPrefix(res3.Header.Get("Content-Type"), "image/jpeg"))
	res4, _ := download(t, small, "")
	assert.Equal(t, 200, res4.StatusCode)
	assert.True(t, strings.HasPrefix(res4.Header.Get("Content-Type"), "image/jpeg"))
	res5, _ := download(t, tiny, "")
	assert.Equal(t, 200, res5.StatusCode)
	assert.True(t, strings.HasPrefix(res5.Header.Get("Content-Type"), "image/jpeg"))
}

func TestThumbnailPDFs(t *testing.T) {
	if testing.Short() {
		return
	}

	f, err := os.Open("../../tests/fixtures/dev-desktop.pdf")
	assert.NoError(t, err)
	defer f.Close()
	req, err := http.NewRequest("POST", ts.URL+"/files/?Type=file&Name=dev-desktop.pdf", f)
	assert.NoError(t, err)
	req.Header.Add(echo.HeaderAuthorization, "Bearer "+token)
	res, obj := doUploadOrMod(t, req, "application/pdf", "")
	assert.Equal(t, 201, res.StatusCode)
	data := obj["data"].(map[string]interface{})
	pdfID := data["id"].(string)

	res2, _ := httpGet(ts.URL + "/files/" + pdfID)
	assert.Equal(t, 200, res2.StatusCode)
	var obj2 map[string]interface{}
	err = extractJSONRes(res2, &obj2)
	assert.NoError(t, err)
	data2 := obj2["data"].(map[string]interface{})
	links := data2["links"].(map[string]interface{})
	large := links["large"].(string)
	medium := links["medium"].(string)
	small := links["small"].(string)
	tiny := links["tiny"].(string)

	// Large, medium, and small are not generated automatically
	res3, _ := download(t, large, "")
	assert.Equal(t, 404, res3.StatusCode)
	assert.Equal(t, res3.Header.Get("Content-Type"), "image/png")
	res4, _ := download(t, medium, "")
	assert.Equal(t, 404, res4.StatusCode)
	assert.Equal(t, res4.Header.Get("Content-Type"), "image/png")
	res5, _ := download(t, small, "")
	assert.Equal(t, 404, res5.StatusCode)
	assert.Equal(t, res5.Header.Get("Content-Type"), "image/png")

	// Wait for tiny thumbnail generation
	time.Sleep(1 * time.Second)

	res6, _ := download(t, tiny, "")
	assert.Equal(t, 200, res6.StatusCode)
	assert.Equal(t, res6.Header.Get("Content-Type"), "image/jpeg")

	// Wait for other thumbnails generation
	time.Sleep(2 * time.Second)

	res7, _ := download(t, large, "")
	assert.Equal(t, 200, res7.StatusCode)
	assert.Equal(t, res7.Header.Get("Content-Type"), "image/jpeg")
	res8, _ := download(t, medium, "")
	assert.Equal(t, 200, res8.StatusCode)
	assert.Equal(t, res8.Header.Get("Content-Type"), "image/jpeg")
	res9, _ := download(t, small, "")
	assert.Equal(t, 200, res9.StatusCode)
	assert.Equal(t, res9.Header.Get("Content-Type"), "image/jpeg")
}

func TestGetFileByPublicLink(t *testing.T) {
	var err error
	body := "foo"
	res1, filedata := upload(t, "/files/?Type=file&Name=publicfile", "text/plain", body, "rL0Y20zC+Fzt72VPzMSk2A==")
	assert.Equal(t, 201, res1.StatusCode)

	var ok bool
	filedata, ok = filedata["data"].(map[string]interface{})
	assert.True(t, ok)

	fileID, ok = filedata["id"].(string)
	assert.True(t, ok)

	// Generating a new token
	publicToken, err = testInstance.MakeJWT(consts.ShareAudience, "email", "io.cozy.files", "", time.Now())
	assert.NoError(t, err)

	expires := time.Now().Add(2 * time.Minute)
	rules := permission.Set{
		permission.Rule{
			Type:   "io.cozy.files",
			Verbs:  permission.Verbs(permission.GET),
			Values: []string{fileID},
		},
	}
	perms := permission.Permission{
		Permissions: rules,
	}
	_, err = permission.CreateShareSet(testInstance, &permission.Permission{Type: "app", Permissions: rules}, "", map[string]string{"email": publicToken}, nil, perms, &expires)
	assert.NoError(t, err)

	req, err := http.NewRequest("GET", ts.URL+"/files/"+fileID, nil)
	assert.NoError(t, err)
	req.Header.Add(echo.HeaderAuthorization, "Bearer "+publicToken)
	res, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	assert.Equal(t, 200, res.StatusCode)
}

func TestGetFileByPublicLinkRateExceeded(t *testing.T) {
	var err error
	// Blocking the file by accessing it a lot of times
	for i := 0; i < 1999; i++ {
		err = limits.CheckRateLimitKey(fileID, limits.SharingPublicLinkType)
		assert.NoError(t, err)
	}

	err = limits.CheckRateLimitKey(fileID, limits.SharingPublicLinkType)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "Rate limit reached")
	req, err := http.NewRequest("GET", ts.URL+"/files/"+fileID, nil)
	assert.NoError(t, err)
	req.Header.Add(echo.HeaderAuthorization, "Bearer "+publicToken)

	res, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	assert.Equal(t, 500, res.StatusCode)
	resbody, err := ioutil.ReadAll(res.Body)
	assert.NoError(t, err)
	assert.Contains(t, string(resbody), "Rate limit exceeded")
}

func TestFind(t *testing.T) {
	type M map[string]interface{}
	type S []interface{}

	defIndex := M{"index": M{"fields": S{"_id"}}}
	_, err := couchdb.DefineIndexRaw(testInstance, "io.cozy.files", &defIndex)
	assert.NoError(t, err)

	defIndex2 := M{"index": M{"fields": S{"type"}}}
	_, err = couchdb.DefineIndexRaw(testInstance, "io.cozy.files", &defIndex2)
	assert.NoError(t, err)

	query := strings.NewReader(`{
		"selector": {
			"type": "file"
		},
		"limit": 1
	}`)
	req, _ := http.NewRequest("POST", ts.URL+"/files/_find", query)
	req.Header.Add(echo.HeaderAuthorization, "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	assert.Equal(t, 200, res.StatusCode)
	var obj map[string]interface{}
	err = extractJSONRes(res, &obj)
	assert.NoError(t, err)

	data := obj["data"].([]interface{})
	meta := obj["meta"].(map[string]interface{})
	if assert.Len(t, data, 1) {
		doc := data[0].(map[string]interface{})
		attrs := doc["attributes"].(map[string]interface{})
		assert.NotEmpty(t, attrs["name"])
		assert.NotEmpty(t, attrs["type"])
		assert.NotEmpty(t, attrs["size"])
		assert.NotEmpty(t, attrs["path"])
	}
	assert.NotNil(t, meta)
	assert.Nil(t, meta["execution_stats"])

	query2 := strings.NewReader(`{
		"selector": {
			"_id": {
				"$gt": null
			}
		},
		"limit": 1,
		"execution_stats": true
	}`)
	req, _ = http.NewRequest("POST", ts.URL+"/files/_find", query2)
	req.Header.Add(echo.HeaderAuthorization, "Bearer "+token)
	res, err = http.DefaultClient.Do(req)
	assert.NoError(t, err)
	assert.Equal(t, 200, res.StatusCode)
	err = extractJSONRes(res, &obj)
	assert.NoError(t, err)
	data = obj["data"].([]interface{})
	meta = obj["meta"].(map[string]interface{})
	assert.Equal(t, len(data), 1)
	id1 := data[0].(map[string]interface{})["id"]
	assert.NotNil(t, meta)
	assert.NotEmpty(t, meta["execution_stats"])
	links := obj["links"].(map[string]interface{})
	next := links["next"].(string)

	query2 = strings.NewReader(`{
		"selector": {
			"_id": {
				"$gt": null
			}
		},
		"limit": 1,
		"execution_stats": true
	}`)
	req, _ = http.NewRequest("POST", ts.URL+next, query2)
	req.Header.Add(echo.HeaderAuthorization, "Bearer "+token)
	res, err = http.DefaultClient.Do(req)
	assert.NoError(t, err)
	assert.Equal(t, 200, res.StatusCode)
	err = extractJSONRes(res, &obj)
	assert.NoError(t, err)
	data = obj["data"].([]interface{})
	assert.Equal(t, len(data), 1)
	id2 := data[0].(map[string]interface{})["id"]
	assert.NotEqual(t, id1, id2)

	query3 := strings.NewReader(`{
		"selector": {
			"_id": {
				"$gt": null
			}
		},
		"fields": ["dir_id", "name", "name"],
		"limit": 1
	}`)
	req, _ = http.NewRequest("POST", ts.URL+"/files/_find", query3)
	req.Header.Add(echo.HeaderAuthorization, "Bearer "+token)
	res, err = http.DefaultClient.Do(req)
	assert.NoError(t, err)
	assert.Equal(t, 200, res.StatusCode)

	err = extractJSONRes(res, &obj)
	assert.NoError(t, err)
	resData := obj["data"].([]interface{})
	dataFields := resData[0].(map[string]interface{})
	attrs := dataFields["attributes"].(map[string]interface{})
	assert.NotEmpty(t, attrs["name"].(string))
	assert.NotEmpty(t, attrs["dir_id"].(string))
	assert.NotEmpty(t, attrs["type"].(string))
	assert.Nil(t, attrs["path"])
	assert.Nil(t, attrs["created_at"])
	assert.Nil(t, attrs["updated_at"])
	assert.Nil(t, attrs["tags"])

	query4 := strings.NewReader(`{
		"selector": {
			"type": "file"
		},
		"fields": ["name"],
		"limit": 1
	}`)
	req, _ = http.NewRequest("POST", ts.URL+"/files/_find", query4)
	req.Header.Add(echo.HeaderAuthorization, "Bearer "+token)
	res, err = http.DefaultClient.Do(req)
	assert.NoError(t, err)
	assert.Equal(t, 200, res.StatusCode)

	err = extractJSONRes(res, &obj)
	assert.NoError(t, err)
	resData = obj["data"].([]interface{})
	if assert.Len(t, resData, 1) {
		dataFields = resData[0].(map[string]interface{})
		attrs = dataFields["attributes"].(map[string]interface{})
		assert.NotEmpty(t, attrs["name"].(string))
		assert.NotEmpty(t, attrs["type"].(string))
		assert.NotEmpty(t, attrs["size"].(string))
		assert.False(t, attrs["trashed"].(bool))
		assert.False(t, attrs["encrypted"].(bool))
		assert.Nil(t, attrs["created_at"])
		assert.Nil(t, attrs["updated_at"])
		assert.Nil(t, attrs["tags"])
		assert.Nil(t, attrs["executable"])
		assert.Nil(t, attrs["dir_id"])
		assert.Nil(t, attrs["path"])
	}
}

func TestDirSize(t *testing.T) {
	_, dirdata := createDir(t, "/files/?Type=directory&Name=dirsizeparent")
	dirdata, ok := dirdata["data"].(map[string]interface{})
	assert.True(t, ok)
	parentID, ok := dirdata["id"].(string)
	assert.True(t, ok)

	_, dirdata = createDir(t, "/files/"+parentID+"?Type=directory&Name=dirsizesub")
	dirdata, ok = dirdata["data"].(map[string]interface{})
	assert.True(t, ok)
	subID, ok := dirdata["id"].(string)
	assert.True(t, ok)

	_, dirdata = createDir(t, "/files/"+subID+"?Type=directory&Name=dirsizesubsub")
	dirdata, ok = dirdata["data"].(map[string]interface{})
	assert.True(t, ok)
	subsubID, ok := dirdata["id"].(string)
	assert.True(t, ok)

	nb := 10
	body := "foo"
	for i := 0; i < nb; i++ {
		name := "file" + strconv.Itoa(i)
		upload(t, "/files/"+parentID+"?Type=file&Name="+name, "text/plain", body, "rL0Y20zC+Fzt72VPzMSk2A==")
		upload(t, "/files/"+subID+"?Type=file&Name="+name, "text/plain", body, "rL0Y20zC+Fzt72VPzMSk2A==")
		upload(t, "/files/"+subsubID+"?Type=file&Name="+name, "text/plain", body, "rL0Y20zC+Fzt72VPzMSk2A==")
	}

	var result struct {
		Data struct {
			Type       string
			ID         string
			Attributes struct {
				Size string
			}
		}
	}
	err := getJSON(t, "/files/"+subsubID+"/size", &result)
	assert.NoError(t, err)
	assert.Equal(t, consts.DirSizes, result.Data.Type)
	assert.Equal(t, subsubID, result.Data.ID)
	assert.Equal(t, "30", result.Data.Attributes.Size)

	err = getJSON(t, "/files/"+subID+"/size", &result)
	assert.NoError(t, err)
	assert.Equal(t, consts.DirSizes, result.Data.Type)
	assert.Equal(t, subID, result.Data.ID)
	assert.Equal(t, "60", result.Data.Attributes.Size)

	err = getJSON(t, "/files/"+parentID+"/size", &result)
	assert.NoError(t, err)
	assert.Equal(t, consts.DirSizes, result.Data.Type)
	assert.Equal(t, parentID, result.Data.ID)
	assert.Equal(t, "90", result.Data.Attributes.Size)
}

func TestDeprecatePreviewAndIcon(t *testing.T) {
	testutils.TODO(t, "2022-09-01", "Remove the deprecated preview and icon for PDF files")
}

func TestMain(m *testing.M) {
	config.UseTestFile()
	testutils.NeedCouchdb()
	setup = testutils.NewSetup(m, "files_test")

	tempdir, err := ioutil.TempDir("", "cozy-stack")
	if err != nil {
		fmt.Println("Could not create temporary directory.")
		os.Exit(1)
	}
	setup.AddCleanup(func() error { return os.RemoveAll(tempdir) })

	config.GetConfig().Fs.URL = &url.URL{
		Scheme: "file",
		Host:   "localhost",
		Path:   tempdir,
	}

	testInstance = setup.GetTestInstance()
	client, tok := setup.GetTestClient(consts.Files + " " + consts.CertifiedCarbonCopy + " " + consts.CertifiedElectronicSafe)
	clientID = client.ClientID
	token = tok
	ts = setup.GetTestServer("/files", Routes, func(r *echo.Echo) *echo.Echo {
		secure := middlewares.Secure(&middlewares.SecureConfig{
			CSPDefaultSrc:     []middlewares.CSPSource{middlewares.CSPSrcSelf},
			CSPFrameAncestors: []middlewares.CSPSource{middlewares.CSPSrcNone},
		})
		r.Use(secure)
		return r
	})
	ts.Config.Handler.(*echo.Echo).HTTPErrorHandler = errors.ErrorHandler

	os.Exit(setup.Run())
}

func httpGet(url string) (*http.Response, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Add(echo.HeaderAuthorization, "Bearer "+token)
	return http.DefaultClient.Do(req)
}
