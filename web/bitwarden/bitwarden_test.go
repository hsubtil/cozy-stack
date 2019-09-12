package bitwarden

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"

	"github.com/cozy/cozy-stack/model/instance"
	"github.com/cozy/cozy-stack/model/instance/lifecycle"
	"github.com/cozy/cozy-stack/pkg/config/config"
	"github.com/cozy/cozy-stack/pkg/consts"
	"github.com/cozy/cozy-stack/pkg/crypto"
	"github.com/cozy/cozy-stack/tests/testutils"
	"github.com/cozy/cozy-stack/web/errors"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
)

var ts *httptest.Server
var inst *instance.Instance
var token string
var folderID, cipherID string

func TestPrelogin(t *testing.T) {
	body := `{ "email": "me@cozy.example.net" }`
	req, _ := http.NewRequest("POST", ts.URL+"/bitwarden/api/accounts/prelogin", bytes.NewBufferString(body))
	req.Header.Add("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	assert.Equal(t, 200, res.StatusCode)
	var result map[string]int
	err = json.NewDecoder(res.Body).Decode(&result)
	assert.NoError(t, err)
	assert.Equal(t, 0, result["Kdf"])
	assert.Equal(t, crypto.DefaultPBKDF2Iterations, result["KdfIterations"])
}

func TestConnect(t *testing.T) {
	email := inst.PassphraseSalt()
	iter := crypto.DefaultPBKDF2Iterations
	pass, _ := crypto.HashPassWithPBKDF2([]byte("cozy"), email, iter)
	v := url.Values{
		"grant_type": {"password"},
		"username":   {string(email)},
		"password":   {string(pass)},
		"scope":      {"api offline_access"},
		"client_id":  {"browser"},
		"deviceType": {"3"},
	}
	res, err := http.PostForm(ts.URL+"/bitwarden/identity/connect/token", v)
	assert.NoError(t, err)
	assert.Equal(t, 200, res.StatusCode)
	var result map[string]interface{}
	err = json.NewDecoder(res.Body).Decode(&result)
	assert.NoError(t, err)
	expiresIn := consts.AccessTokenValidityDuration.Seconds()
	assert.Equal(t, "Bearer", result["token_type"])
	assert.Equal(t, expiresIn, result["expires_in"])
	if assert.NotEmpty(t, result["access_token"]) {
		token = result["access_token"].(string)
	}
	assert.NotEmpty(t, result["refresh_token"])
	assert.NotEmpty(t, result["Key"])
}

func TestCreateFolder(t *testing.T) {
	body := `
{
	"name": "2.FQAwIBaDbczEGnEJw4g4hw==|7KreXaC0duAj0ulzZJ8ncA==|nu2sEvotjd4zusvGF8YZJPnS9SiJPDqc1VIfCrfve/o="
}`
	req, _ := http.NewRequest("POST", ts.URL+"/bitwarden/api/folders", bytes.NewBufferString(body))
	req.Header.Add("Content-Type", "application/json")
	req.Header.Add("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	assert.Equal(t, 200, res.StatusCode)
	var result map[string]string
	err = json.NewDecoder(res.Body).Decode(&result)
	assert.NoError(t, err)
	assert.Equal(t, "2.FQAwIBaDbczEGnEJw4g4hw==|7KreXaC0duAj0ulzZJ8ncA==|nu2sEvotjd4zusvGF8YZJPnS9SiJPDqc1VIfCrfve/o=", result["Name"])
	assert.Equal(t, "folder", result["Object"])
	assert.NotEmpty(t, result["RevisionDate"])
	assert.NotEmpty(t, result["Id"])
	folderID = result["Id"]
}

func TestListFolders(t *testing.T) {
	req, _ := http.NewRequest("GET", ts.URL+"/bitwarden/api/folders", nil)
	req.Header.Add("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	assert.Equal(t, 200, res.StatusCode)
	var result map[string]interface{}
	err = json.NewDecoder(res.Body).Decode(&result)
	assert.NoError(t, err)
	assert.Equal(t, "list", result["Object"])
	data := result["Data"].([]interface{})
	assert.Len(t, data, 1)
	item := data[0].(map[string]interface{})
	assert.Equal(t, "2.FQAwIBaDbczEGnEJw4g4hw==|7KreXaC0duAj0ulzZJ8ncA==|nu2sEvotjd4zusvGF8YZJPnS9SiJPDqc1VIfCrfve/o=", item["Name"])
	assert.Equal(t, "folder", item["Object"])
	assert.Equal(t, folderID, item["Id"])
	assert.NotEmpty(t, item["RevisionDate"])
}

func TestGetFolder(t *testing.T) {
	req, _ := http.NewRequest("GET", ts.URL+"/bitwarden/api/folders/"+folderID, nil)
	req.Header.Add("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	assert.Equal(t, 200, res.StatusCode)
	var result map[string]interface{}
	err = json.NewDecoder(res.Body).Decode(&result)
	assert.NoError(t, err)
	assert.Equal(t, "2.FQAwIBaDbczEGnEJw4g4hw==|7KreXaC0duAj0ulzZJ8ncA==|nu2sEvotjd4zusvGF8YZJPnS9SiJPDqc1VIfCrfve/o=", result["Name"])
	assert.Equal(t, "folder", result["Object"])
	assert.Equal(t, folderID, result["Id"])
	assert.NotEmpty(t, result["RevisionDate"])
}

func TestRenameFolder(t *testing.T) {
	body := `
{
	"name": "2.d7MttWzJTSSKx1qXjHUxlQ==|01Ath5UqFZHk7csk5DVtkQ==|EMLoLREgCUP5Cu4HqIhcLqhiZHn+NsUDp8dAg1Xu0Io="
}`
	req, _ := http.NewRequest("PUT", ts.URL+"/bitwarden/api/folders/"+folderID, bytes.NewBufferString(body))
	req.Header.Add("Content-Type", "application/json")
	req.Header.Add("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	assert.Equal(t, 200, res.StatusCode)
	var result map[string]string
	err = json.NewDecoder(res.Body).Decode(&result)
	assert.NoError(t, err)
	assert.Equal(t, "2.d7MttWzJTSSKx1qXjHUxlQ==|01Ath5UqFZHk7csk5DVtkQ==|EMLoLREgCUP5Cu4HqIhcLqhiZHn+NsUDp8dAg1Xu0Io=", result["Name"])
	assert.Equal(t, "folder", result["Object"])
	assert.NotEmpty(t, result["RevisionDate"])
	assert.Equal(t, folderID, result["Id"])
}

func TestDeleteFolder(t *testing.T) {
	body := `
{
	"name": "2.FQAwIBaDbczEGnEJw4g4hw==|7KreXaC0duAj0ulzZJ8ncA==|nu2sEvotjd4zusvGF8YZJPnS9SiJPDqc1VIfCrfve/o="
}`
	req, _ := http.NewRequest("POST", ts.URL+"/bitwarden/api/folders", bytes.NewBufferString(body))
	req.Header.Add("Content-Type", "application/json")
	req.Header.Add("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	assert.Equal(t, 200, res.StatusCode)
	var result map[string]string
	err = json.NewDecoder(res.Body).Decode(&result)
	assert.NoError(t, err)
	id := result["Id"]

	body = `
{
	"type": 1,
	"favorite": false,
	"name": "2.d7MttWzJTSSKx1qXjHUxlQ==|01Ath5UqFZHk7csk5DVtkQ==|EMLoLREgCUP5Cu4HqIhcLqhiZHn+NsUDp8dAg1Xu0Io=",
	"notes": null,
	"folderId": "` + id + `",
	"organizationId": null,
	"login": {
		"uri": "2.T57BwAuV8ubIn/sZPbQC+A==|EhUSSpJWSzSYOdJ/AQzfXuUXxwzcs/6C4tOXqhWAqcM=|OWV2VIqLfoWPs9DiouXGUOtTEkVeklbtJQHkQFIXkC8=",
		"username": "2.JbFkAEZPnuMm70cdP44wtA==|fsN6nbT+udGmOWv8K4otgw==|JbtwmNQa7/48KszT2hAdxpmJ6DRPZst0EDEZx5GzesI=",
		"password": "2.e83hIsk6IRevSr/H1lvZhg==|48KNkSCoTacopXRmIZsbWg==|CIcWgNbaIN2ix2Fx1Gar6rWQeVeboehp4bioAwngr0o=",
		"totp": null
	}
}`
	req, _ = http.NewRequest("POST", ts.URL+"/bitwarden/api/ciphers", bytes.NewBufferString(body))
	req.Header.Add("Content-Type", "application/json")
	req.Header.Add("Authorization", "Bearer "+token)
	res, err = http.DefaultClient.Do(req)
	assert.NoError(t, err)
	assert.Equal(t, 200, res.StatusCode)
	var result2 map[string]interface{}
	err = json.NewDecoder(res.Body).Decode(&result2)
	assert.NoError(t, err)
	cID := result2["Id"].(string)

	req, _ = http.NewRequest("DELETE", ts.URL+"/bitwarden/api/folders/"+id, nil)
	req.Header.Add("Authorization", "Bearer "+token)
	res, err = http.DefaultClient.Do(req)
	assert.NoError(t, err)
	assert.Equal(t, 204, res.StatusCode)

	// Check that the cipher in this folder has been moved out
	req, _ = http.NewRequest("GET", ts.URL+"/bitwarden/api/ciphers/"+cID, nil)
	req.Header.Add("Authorization", "Bearer "+token)
	res, err = http.DefaultClient.Do(req)
	assert.NoError(t, err)
	assert.Equal(t, 200, res.StatusCode)
	var result3 map[string]interface{}
	err = json.NewDecoder(res.Body).Decode(&result3)
	assert.NoError(t, err)
	assert.Equal(t, "2.d7MttWzJTSSKx1qXjHUxlQ==|01Ath5UqFZHk7csk5DVtkQ==|EMLoLREgCUP5Cu4HqIhcLqhiZHn+NsUDp8dAg1Xu0Io=", result3["Name"])
	fID, ok := result3["FolderId"]
	assert.True(t, ok)
	assert.Empty(t, fID)

	req, _ = http.NewRequest("DELETE", ts.URL+"/bitwarden/api/ciphers/"+cID, nil)
	req.Header.Add("Authorization", "Bearer "+token)
	res, err = http.DefaultClient.Do(req)
	assert.NoError(t, err)
	assert.Equal(t, 204, res.StatusCode)
}

func TestCreateLogin(t *testing.T) {
	body := `
{
	"type": 1,
	"favorite": false,
	"name": "2.d7MttWzJTSSKx1qXjHUxlQ==|01Ath5UqFZHk7csk5DVtkQ==|EMLoLREgCUP5Cu4HqIhcLqhiZHn+NsUDp8dAg1Xu0Io=",
	"notes": null,
	"folderId": null,
	"organizationId": null,
	"login": {
		"uri": "2.T57BwAuV8ubIn/sZPbQC+A==|EhUSSpJWSzSYOdJ/AQzfXuUXxwzcs/6C4tOXqhWAqcM=|OWV2VIqLfoWPs9DiouXGUOtTEkVeklbtJQHkQFIXkC8=",
		"username": "2.JbFkAEZPnuMm70cdP44wtA==|fsN6nbT+udGmOWv8K4otgw==|JbtwmNQa7/48KszT2hAdxpmJ6DRPZst0EDEZx5GzesI=",
		"password": "2.e83hIsk6IRevSr/H1lvZhg==|48KNkSCoTacopXRmIZsbWg==|CIcWgNbaIN2ix2Fx1Gar6rWQeVeboehp4bioAwngr0o=",
		"totp": null
	}
}`
	req, _ := http.NewRequest("POST", ts.URL+"/bitwarden/api/ciphers", bytes.NewBufferString(body))
	req.Header.Add("Content-Type", "application/json")
	req.Header.Add("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	assert.Equal(t, 200, res.StatusCode)
	var result map[string]interface{}
	err = json.NewDecoder(res.Body).Decode(&result)
	assert.NoError(t, err)
	assertCipherResponse(t, result)
	cipherID = result["Id"].(string)
}

func TestListCiphers(t *testing.T) {
	req, _ := http.NewRequest("GET", ts.URL+"/bitwarden/api/ciphers", nil)
	req.Header.Add("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	assert.Equal(t, 200, res.StatusCode)
	var result map[string]interface{}
	err = json.NewDecoder(res.Body).Decode(&result)
	assert.NoError(t, err)
	assert.Equal(t, "list", result["Object"])
	data := result["Data"].([]interface{})
	assert.Len(t, data, 1)
	item := data[0].(map[string]interface{})
	assertCipherResponse(t, item)
}

func TestGetCipher(t *testing.T) {
	req, _ := http.NewRequest("GET", ts.URL+"/bitwarden/api/ciphers/"+cipherID, nil)
	req.Header.Add("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	assert.Equal(t, 200, res.StatusCode)
	var result map[string]interface{}
	err = json.NewDecoder(res.Body).Decode(&result)
	assert.NoError(t, err)
	assertCipherResponse(t, result)
}

func assertCipherResponse(t *testing.T, result map[string]interface{}) {
	assert.Equal(t, "cipher", result["Object"])
	assert.NotEmpty(t, result["Id"])
	assert.Equal(t, float64(1), result["Type"])
	assert.Equal(t, false, result["Favorite"])
	assert.Equal(t, "2.d7MttWzJTSSKx1qXjHUxlQ==|01Ath5UqFZHk7csk5DVtkQ==|EMLoLREgCUP5Cu4HqIhcLqhiZHn+NsUDp8dAg1Xu0Io=", result["Name"])
	notes, ok := result["Notes"]
	assert.True(t, ok)
	assert.Empty(t, notes)
	fID, ok := result["FolderId"]
	assert.True(t, ok)
	assert.Empty(t, fID)
	orgID, ok := result["OrganizationId"]
	assert.True(t, ok)
	assert.Empty(t, orgID)
	login := result["Login"].(map[string]interface{})
	uris := login["Uris"].([]interface{})
	assert.Len(t, uris, 1)
	uri := uris[0].(map[string]interface{})
	assert.Equal(t, "2.T57BwAuV8ubIn/sZPbQC+A==|EhUSSpJWSzSYOdJ/AQzfXuUXxwzcs/6C4tOXqhWAqcM=|OWV2VIqLfoWPs9DiouXGUOtTEkVeklbtJQHkQFIXkC8=", uri["Uri"])
	match, ok := uri["Match"]
	assert.True(t, ok)
	assert.Empty(t, match)
	assert.Equal(t, "2.JbFkAEZPnuMm70cdP44wtA==|fsN6nbT+udGmOWv8K4otgw==|JbtwmNQa7/48KszT2hAdxpmJ6DRPZst0EDEZx5GzesI=", result["Username"])
	assert.Equal(t, "2.e83hIsk6IRevSr/H1lvZhg==|48KNkSCoTacopXRmIZsbWg==|CIcWgNbaIN2ix2Fx1Gar6rWQeVeboehp4bioAwngr0o=", result["Password"])
	totp, ok := result["Totp"]
	assert.True(t, ok)
	assert.Empty(t, totp)
	fields, ok := result["Fields"]
	assert.True(t, ok)
	assert.Empty(t, fields)
	attachments, ok := result["Attachments"]
	assert.True(t, ok)
	assert.Empty(t, attachments)
	assert.NotEmpty(t, result["RevisionDate"])
	assert.Equal(t, true, result["Edit"])
	assert.Equal(t, false, result["OrganizationUseTotp"])
}

func TestUpdateCipher(t *testing.T) {
	body := `
{
	"type": 2,
	"favorite": true,
	"name": "2.G38TIU3t1pGOfkzjCQE7OQ==|Xa1RupttU7zrWdzIT6oK+w==|J3C6qU1xDrfTgyJD+OrDri1GjgGhU2nmRK75FbZHXoI=",
	"folderId": "` + folderID + `",
	"organizationId": null,
	"notes": "2.rSw0uVQEFgUCEmOQx0JnDg==|MKqHLD25aqaXYHeYJPH/mor7l3EeSQKsI7A/R+0bFTI=|ODcUScISzKaZWHlUe4MRGuTT2S7jpyDmbOHl7d+6HiM=",
	"secureNote": {
		"type": 0
	}
}`
	req, _ := http.NewRequest("PUT", ts.URL+"/bitwarden/api/ciphers/"+cipherID, bytes.NewBufferString(body))
	req.Header.Add("Content-Type", "application/json")
	req.Header.Add("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	assert.Equal(t, 200, res.StatusCode)
	var result map[string]interface{}
	err = json.NewDecoder(res.Body).Decode(&result)
	assert.NoError(t, err)
	assertUpdatedCipherResponse(t, result)
}

func assertUpdatedCipherResponse(t *testing.T, result map[string]interface{}) {
	assert.Equal(t, "cipher", result["Object"])
	assert.Equal(t, cipherID, result["Id"])
	assert.Equal(t, float64(2), result["Type"])
	assert.Equal(t, true, result["Favorite"])
	assert.Equal(t, "2.G38TIU3t1pGOfkzjCQE7OQ==|Xa1RupttU7zrWdzIT6oK+w==|J3C6qU1xDrfTgyJD+OrDri1GjgGhU2nmRK75FbZHXoI=", result["Name"])
	assert.Equal(t, folderID, result["FolderId"])
	assert.Equal(t, "2.rSw0uVQEFgUCEmOQx0JnDg==|MKqHLD25aqaXYHeYJPH/mor7l3EeSQKsI7A/R+0bFTI=|ODcUScISzKaZWHlUe4MRGuTT2S7jpyDmbOHl7d+6HiM=", result["Notes"])
	secure := result["SecureNote"].(map[string]interface{})
	assert.Equal(t, float64(0), secure["Type"])
	orgID, ok := result["OrganizationId"]
	assert.True(t, ok)
	assert.Empty(t, orgID)
	_, ok = result["Login"]
	assert.False(t, ok)
	fields, ok := result["Fields"]
	assert.True(t, ok)
	assert.Empty(t, fields)
	attachments, ok := result["Attachments"]
	assert.True(t, ok)
	assert.Empty(t, attachments)
	assert.NotEmpty(t, result["RevisionDate"])
	assert.Equal(t, true, result["Edit"])
	assert.Equal(t, false, result["OrganizationUseTotp"])
}

func TestDeleteCipher(t *testing.T) {
	body := `
{
	"type": 1,
	"favorite": false,
	"name": "2.d7MttWzJTSSKx1qXjHUxlQ==|01Ath5UqFZHk7csk5DVtkQ==|EMLoLREgCUP5Cu4HqIhcLqhiZHn+NsUDp8dAg1Xu0Io=",
	"notes": null,
	"folderId": null,
	"organizationId": null,
	"login": {
		"uri": "2.T57BwAuV8ubIn/sZPbQC+A==|EhUSSpJWSzSYOdJ/AQzfXuUXxwzcs/6C4tOXqhWAqcM=|OWV2VIqLfoWPs9DiouXGUOtTEkVeklbtJQHkQFIXkC8=",
		"username": "2.JbFkAEZPnuMm70cdP44wtA==|fsN6nbT+udGmOWv8K4otgw==|JbtwmNQa7/48KszT2hAdxpmJ6DRPZst0EDEZx5GzesI=",
		"password": "2.e83hIsk6IRevSr/H1lvZhg==|48KNkSCoTacopXRmIZsbWg==|CIcWgNbaIN2ix2Fx1Gar6rWQeVeboehp4bioAwngr0o=",
		"totp": null
	}
}`
	req, _ := http.NewRequest("POST", ts.URL+"/bitwarden/api/ciphers", bytes.NewBufferString(body))
	req.Header.Add("Content-Type", "application/json")
	req.Header.Add("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	assert.Equal(t, 200, res.StatusCode)
	var result map[string]interface{}
	err = json.NewDecoder(res.Body).Decode(&result)
	assert.NoError(t, err)
	id := result["Id"].(string)

	req, _ = http.NewRequest("DELETE", ts.URL+"/bitwarden/api/ciphers/"+id, nil)
	req.Header.Add("Authorization", "Bearer "+token)
	res, err = http.DefaultClient.Do(req)
	assert.NoError(t, err)
	assert.Equal(t, 204, res.StatusCode)
}

func TestSync(t *testing.T) {
	req, _ := http.NewRequest("GET", ts.URL+"/bitwarden/api/sync", nil)
	req.Header.Add("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	assert.Equal(t, 200, res.StatusCode)
	var result map[string]interface{}
	err = json.NewDecoder(res.Body).Decode(&result)
	assert.NoError(t, err)
	assert.Equal(t, "sync", result["Object"])

	profile := result["Profile"].(map[string]interface{})
	assert.NotEmpty(t, profile["Id"])
	assert.Equal(t, "Pierre", profile["Name"])
	assert.Equal(t, "me@cozy.example.net", profile["Email"])
	assert.Equal(t, false, profile["EmailVerified"])
	assert.Equal(t, true, profile["Premium"])
	assert.Equal(t, nil, profile["MasterPasswordHint"])
	assert.Equal(t, "en", profile["Culture"])
	assert.Equal(t, false, profile["TwoFactorEnabled"])
	assert.NotEmpty(t, profile["Key"])
	assert.Equal(t, nil, profile["PrivateKey"])
	assert.NotEmpty(t, profile["SecurityStamp"])
	assert.Equal(t, "profile", profile["Object"])

	ciphers := result["Ciphers"].([]interface{})
	assert.Len(t, ciphers, 1)
	c := ciphers[0].(map[string]interface{})
	assertUpdatedCipherResponse(t, c)

	folders := result["Folders"].([]interface{})
	assert.Len(t, folders, 1)
	f := folders[0].(map[string]interface{})
	assert.Equal(t, "2.d7MttWzJTSSKx1qXjHUxlQ==|01Ath5UqFZHk7csk5DVtkQ==|EMLoLREgCUP5Cu4HqIhcLqhiZHn+NsUDp8dAg1Xu0Io=", f["Name"])
	assert.Equal(t, "folder", f["Object"])
	assert.NotEmpty(t, f["RevisionDate"])
	assert.Equal(t, folderID, f["Id"])

	domains := result["Domains"].(map[string]interface{})
	ed, ok := domains["EquivalentDomains"]
	assert.True(t, ok)
	assert.Empty(t, ed)
	ged, ok := domains["GlobalEquivalentDomains"]
	assert.True(t, ok)
	assert.Empty(t, ged)
	assert.Equal(t, "domains", domains["Object"])
}

func TestChangeSecurityHash(t *testing.T) {
	email := inst.PassphraseSalt()
	iter := crypto.DefaultPBKDF2Iterations
	pass, _ := crypto.HashPassWithPBKDF2([]byte("cozy"), email, iter)
	body, _ := json.Marshal(map[string]string{
		"masterPasswordHash": string(pass),
	})
	buf := bytes.NewBuffer(body)
	res, err := http.Post(ts.URL+"/bitwarden/api/accounts/security-stamp", "application/json", buf)
	assert.NoError(t, err)
	assert.Equal(t, 204, res.StatusCode)

	// Check that token is no longer valid
	req, _ := http.NewRequest("GET", ts.URL+"/bitwarden/api/folders", nil)
	req.Header.Add("Authorization", "Bearer "+token)
	res, err = http.DefaultClient.Do(req)
	assert.NoError(t, err)
	assert.Equal(t, 401, res.StatusCode)
}

func TestMain(m *testing.M) {
	config.UseTestFile()
	testutils.NeedCouchdb()
	setup := testutils.NewSetup(m, "bitwarden_test")
	inst = setup.GetTestInstance(&lifecycle.Options{
		Domain:     "cozy.example.net",
		Passphrase: "cozy",
		PublicName: "Pierre",
	})

	ts = setup.GetTestServer("/bitwarden", Routes)
	ts.Config.Handler.(*echo.Echo).HTTPErrorHandler = errors.ErrorHandler
	os.Exit(setup.Run())
}
