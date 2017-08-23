package exec

import (
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/cozy/cozy-stack/pkg/apps"
	"github.com/cozy/cozy-stack/pkg/config"
	"github.com/cozy/cozy-stack/pkg/consts"
	"github.com/cozy/cozy-stack/pkg/couchdb"
	"github.com/cozy/cozy-stack/pkg/crypto"
	"github.com/cozy/cozy-stack/pkg/instance"
	"github.com/cozy/cozy-stack/pkg/jobs"
	"github.com/cozy/cozy-stack/pkg/permissions"
	"github.com/cozy/cozy-stack/pkg/realtime"
	"github.com/cozy/cozy-stack/tests/testutils"
	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	jwt "gopkg.in/dgrijalva/jwt-go.v3"
)

var inst *instance.Instance

var konnectorWorkerFunc = makeExecWorkerFunc()

func TestUnknownDomain(t *testing.T) {
	ctx := jobs.NewWorkerContext("unknown", "id")
	msg, err := jobs.NewMessage(jobs.JSONEncoding, map[string]interface{}{
		"konnector": "unknownapp",
	})
	assert.NoError(t, err)
	w := &konnectorWorker{}
	err = konnectorWorkerFunc(ctx, w, msg)
	assert.Error(t, err)
	assert.Equal(t, "Instance not found", err.Error())
}

func TestUnknownApp(t *testing.T) {
	ctx := jobs.NewWorkerContext(inst.Domain, "id")
	msg, err := jobs.NewMessage(jobs.JSONEncoding, map[string]interface{}{
		"konnector": "unknownapp",
	})
	assert.NoError(t, err)
	w := &konnectorWorker{}
	err = konnectorWorkerFunc(ctx, w, msg)
	assert.Error(t, err)
	assert.Equal(t, "Application is not installed", err.Error())
}

func TestBadFileExec(t *testing.T) {
	account := "123456"
	folderToSave := "7890"

	installer, err := apps.NewInstaller(inst, inst.AppsCopier(apps.Konnector),
		&apps.InstallerOptions{
			Operation: apps.Install,
			Type:      apps.Konnector,
			Slug:      "my-konnector-1",
			SourceURL: "git://github.com/cozy/cozy-konnector-trainline.git",
		},
	)
	if !assert.NoError(t, err) {
		return
	}
	_, err = installer.RunSync()
	if !assert.NoError(t, err) {
		return
	}

	ctx := jobs.NewWorkerContext(inst.Domain, "id")
	msg, err := jobs.NewMessage(jobs.JSONEncoding, map[string]interface{}{
		"konnector":      "my-konnector-1",
		"account":        account,
		"folder_to_save": folderToSave,
	})
	assert.NoError(t, err)

	config.GetConfig().Konnectors.Cmd = ""
	w := &konnectorWorker{}
	err = konnectorWorkerFunc(ctx, w, msg)
	assert.Error(t, err)
	assert.Equal(t, "fork/exec : no such file or directory", err.Error())

	config.GetConfig().Konnectors.Cmd = "echo"
	w = &konnectorWorker{}
	err = konnectorWorkerFunc(ctx, w, msg)
	assert.NoError(t, err)
}

func TestSuccess(t *testing.T) {
	script := `#!/bin/bash

echo "{\"type\": \"toto\", \"message\": \"COZY_URL=${COZY_URL} ${COZY_CREDENTIALS}\"}"
echo "bad json"
echo "{\"type\": \"manifest\", \"message\": \"$(ls ${1}/manifest.konnector)\" }"
>&2 echo "log error"
`
	osFs := afero.NewOsFs()
	tmpScript, err := afero.TempFile(osFs, "", "")
	if !assert.NoError(t, err) {
		return
	}
	defer osFs.RemoveAll(tmpScript.Name())

	err = afero.WriteFile(osFs, tmpScript.Name(), []byte(script), 0)
	if !assert.NoError(t, err) {
		return
	}

	err = osFs.Chmod(tmpScript.Name(), 0777)
	if !assert.NoError(t, err) {
		return
	}

	account := "123456"

	installer, err := apps.NewInstaller(inst, inst.AppsCopier(apps.Konnector),
		&apps.InstallerOptions{
			Operation: apps.Install,
			Type:      apps.Konnector,
			Slug:      "my-konnector-2",
			SourceURL: "git://github.com/cozy/cozy-konnector-trainline.git",
		},
	)
	if !assert.NoError(t, err) {
		return
	}
	_, err = installer.RunSync()
	if !assert.NoError(t, err) {
		return
	}

	var wg sync.WaitGroup
	wg.Add(1)

	go func() {
		evCh := realtime.GetHub().Subscriber(inst.Domain)
		evCh.Subscribe(consts.JobEvents)
		ch := evCh.Channel
		ev1 := <-ch
		ev2 := <-ch
		err = evCh.Close()
		assert.NoError(t, err)
		doc1 := ev1.Doc.(couchdb.JSONDoc)
		doc2 := ev2.Doc.(couchdb.JSONDoc)

		assert.Equal(t, inst.Domain, ev1.Domain)
		assert.Equal(t, inst.Domain, ev2.Domain)

		assert.Equal(t, "toto", doc1.M["type"])
		assert.Equal(t, "manifest", doc2.M["type"])

		msg2 := doc2.M["message"].(string)
		assert.True(t, strings.HasPrefix(msg2, os.TempDir()))
		assert.True(t, strings.HasSuffix(msg2, "/manifest.konnector"))

		msg1 := doc1.M["message"].(string)
		cozyURL := "COZY_URL=" + inst.PageURL("/", nil) + " "
		assert.True(t, strings.HasPrefix(msg1, cozyURL))
		token := msg1[len(cozyURL):]
		var claims permissions.Claims
		err = crypto.ParseJWT(token, func(t *jwt.Token) (interface{}, error) {
			return inst.PickKey(t.Claims.(*permissions.Claims).Audience)
		}, &claims)
		assert.NoError(t, err)
		assert.Equal(t, permissions.KonnectorAudience, claims.Audience)
		wg.Done()
	}()

	ctx := jobs.NewWorkerContext(inst.Domain, "id")
	msg, err := jobs.NewMessage(jobs.JSONEncoding, map[string]interface{}{
		"konnector": "my-konnector-2",
		"account":   account,
	})
	assert.NoError(t, err)

	config.GetConfig().Konnectors.Cmd = tmpScript.Name()
	w := &konnectorWorker{}
	err = konnectorWorkerFunc(ctx, w, msg)
	assert.NoError(t, err)

	wg.Wait()
}

func TestMain(m *testing.M) {
	config.UseTestFile()
	setup := testutils.NewSetup(m, "konnector_test")
	inst = setup.GetTestInstance()
	os.Exit(setup.Run())
}
