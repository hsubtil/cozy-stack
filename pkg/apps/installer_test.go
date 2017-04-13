package apps

import (
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/cozy/checkup"
	"github.com/cozy/cozy-stack/pkg/config"
	"github.com/cozy/cozy-stack/pkg/consts"
	"github.com/cozy/cozy-stack/pkg/couchdb"
	"github.com/spf13/afero"
)

var localGitCmd *exec.Cmd
var localGitDir string
var localVersion string
var ts *httptest.Server

var manGen func() string
var manName string

type transport struct{}

func (t *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	req2 := new(http.Request)
	*req2 = *req
	req2.URL, _ = url.Parse(ts.URL)
	return http.DefaultTransport.RoundTrip(req2)
}

func manifestWebapp() string {
	return strings.Replace(`{
  "description": "A mini app to test cozy-stack-v2",
  "developer": {
    "name": "Bruno",
    "url": "cozy.io"
  },
  "license": "MIT",
  "name": "mini-app",
  "permissions": {},
  "slug": "mini",
  "version": "`+localVersion+`"
}`, "\n", "", -1)
}

func manifestKonnector() string {
	return strings.Replace(`{
  "description": "A mini konnector to test cozy-stack-v2",
  "type": "node",
  "developer": {
    "name": "Bruno",
    "url": "cozy.io"
  },
  "license": "MIT",
  "name": "mini-app",
  "permissions": {},
  "slug": "mini",
  "version": "`+localVersion+`"
}`, "\n", "", -1)
}

func serveGitRep() {
	dir, err := ioutil.TempDir("", "cozy-app")
	if err != nil {
		panic(err)
	}
	localGitDir = dir
	args := `
echo '` + manifestWebapp() + `' > ` + WebappManifestName + ` && \
echo '` + manifestKonnector() + `' > ` + KonnectorManifestName + ` && \
git init . && \
git add . && \
git commit -m 'Initial commit' && \
git checkout -b branch && \
echo 'branch' > branch && \
git add . && \
git commit -m 'Create a branch' && \
git checkout -`
	cmd := exec.Command("sh", "-c", args)
	cmd.Dir = localGitDir
	if err := cmd.Run(); err != nil {
		panic(err)
	}

	// "git daemon --reuseaddr --base-path=./ --export-all ./.git"
	localGitCmd = exec.Command("git", "daemon", "--reuseaddr", "--base-path=./", "--export-all", "./.git")
	localGitCmd.Dir = localGitDir
	if out, err := localGitCmd.CombinedOutput(); err != nil {
		fmt.Println(string(out))
	}
}

func doUpgrade(major int) {
	localVersion = fmt.Sprintf("%d.0.0", major)
	args := `
echo '` + manifestWebapp() + `' > ` + WebappManifestName + ` && \
echo '` + manifestKonnector() + `' > ` + KonnectorManifestName + ` && \
git commit -am "Upgrade commit" && \
git checkout branch && \
git rebase master && \
git checkout master`
	cmd := exec.Command("sh", "-c", args)
	cmd.Dir = localGitDir
	if out, err := cmd.Output(); err != nil {
		fmt.Println(string(out), err)
	}
}

var db couchdb.Database
var fs Copier
var baseFS afero.Fs

func TestMain(m *testing.M) {
	config.UseTestFile()

	check, err := checkup.HTTPChecker{URL: config.CouchURL()}.Check()
	if err != nil || check.Status() != checkup.Healthy {
		fmt.Println("This test need couchdb to run.")
		os.Exit(1)
	}

	manifestClient = &http.Client{
		Transport: &transport{},
	}

	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, manGen())
	}))

	db = couchdb.SimpleDatabasePrefix("apps-test")

	err = couchdb.ResetDB(db, consts.Apps)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	err = couchdb.ResetDB(db, consts.Konnectors)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	err = couchdb.ResetDB(db, consts.Files)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	baseFS = afero.NewMemMapFs()
	fs = NewAferoCopier(baseFS)

	go serveGitRep()

	time.Sleep(100 * time.Millisecond)

	err = couchdb.ResetDB(db, consts.Permissions)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	err = couchdb.DefineIndexes(db, consts.IndexesByDoctype(consts.Files))
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	err = couchdb.DefineIndexes(db, consts.IndexesByDoctype(consts.Permissions))
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	res := m.Run()

	couchdb.DeleteDB(db, consts.Apps)
	couchdb.DeleteDB(db, consts.Konnectors)
	couchdb.DeleteDB(db, consts.Files)
	couchdb.DeleteDB(db, consts.Permissions)
	ts.Close()

	localGitCmd.Process.Signal(os.Interrupt)

	os.Exit(res)
}
