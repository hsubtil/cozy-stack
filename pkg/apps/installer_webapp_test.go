package apps

import (
	"fmt"
	"path"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
)

func TestWebappInstallBadSlug(t *testing.T) {
	manGen = manifestWebapp
	manName = WebappManifestName
	_, err := NewInstaller(db, fs, &InstallerOptions{
		Operation: Install,
		Type:      Webapp,
		SourceURL: "git://foo.bar",
	})
	if assert.Error(t, err) {
		assert.Equal(t, ErrInvalidSlugName, err)
	}

	_, err = NewInstaller(db, fs, &InstallerOptions{
		Operation: Install,
		Type:      Webapp,
		Slug:      "coucou/",
		SourceURL: "git://foo.bar",
	})
	if assert.Error(t, err) {
		assert.Equal(t, ErrInvalidSlugName, err)
	}
}

func TestWebappInstallBadAppsSource(t *testing.T) {
	manGen = manifestWebapp
	manName = WebappManifestName
	_, err := NewInstaller(db, fs, &InstallerOptions{
		Operation: Install,
		Type:      Webapp,
		Slug:      "app3",
		SourceURL: "foo://bar.baz",
	})
	if assert.Error(t, err) {
		assert.Equal(t, ErrNotSupportedSource, err)
	}

	_, err = NewInstaller(db, fs, &InstallerOptions{
		Operation: Install,
		Type:      Webapp,
		Slug:      "app4",
		SourceURL: "git://bar  .baz",
	})
	if assert.Error(t, err) {
		assert.Contains(t, err.Error(), "invalid character")
	}

	_, err = NewInstaller(db, fs, &InstallerOptions{
		Operation: Install,
		Type:      Webapp,
		Slug:      "app5",
		SourceURL: "",
	})
	if assert.Error(t, err) {
		assert.Equal(t, ErrMissingSource, err)
	}
}

func TestWebappInstallSuccessful(t *testing.T) {
	manGen = manifestWebapp
	manName = WebappManifestName

	doUpgrade(1)

	inst, err := NewInstaller(db, fs, &InstallerOptions{
		Operation: Install,
		Type:      Webapp,
		Slug:      "local-cozy-mini",
		SourceURL: "git://localhost/",
	})
	if !assert.NoError(t, err) {
		return
	}

	go inst.Run()

	var state State
	var man Manifest
	for {
		var done bool
		var err2 error
		man, done, err2 = inst.Poll()
		if !assert.NoError(t, err2) {
			return
		}
		if state == "" {
			if !assert.EqualValues(t, Installing, man.State()) {
				return
			}
		} else if state == Installing {
			if !assert.EqualValues(t, Ready, man.State()) {
				return
			}
			if !assert.True(t, done) {
				return
			}
			break
		} else {
			t.Fatalf("invalid state")
			return
		}
		state = man.State()
	}

	ok, err := afero.Exists(baseFS, path.Join("/", man.Slug(), man.Version(), WebappManifestName))
	assert.NoError(t, err)
	assert.True(t, ok, "The manifest is present")
	ok, err = afero.FileContainsBytes(baseFS, path.Join("/", man.Slug(), man.Version(), WebappManifestName), []byte("1.0.0"))
	assert.NoError(t, err)
	assert.True(t, ok, "The manifest has the right version")

	inst2, err := NewInstaller(db, fs, &InstallerOptions{
		Operation: Install,
		Type:      Webapp,
		Slug:      "local-cozy-mini",
		SourceURL: "git://localhost/",
	})
	assert.Nil(t, inst2)
	assert.Equal(t, ErrAlreadyExists, err)
}

func TestWebappUpgradeNotExist(t *testing.T) {
	manGen = manifestWebapp
	manName = WebappManifestName
	inst, err := NewInstaller(db, fs, &InstallerOptions{
		Operation: Update,
		Type:      Webapp,
		Slug:      "cozy-app-not-exist",
	})
	assert.Nil(t, inst)
	assert.Equal(t, ErrNotFound, err)

	inst, err = NewInstaller(db, fs, &InstallerOptions{
		Operation: Delete,
		Type:      Webapp,
		Slug:      "cozy-app-not-exist",
	})
	assert.Nil(t, inst)
	assert.Equal(t, ErrNotFound, err)
}

func TestWebappInstallWithUpgrade(t *testing.T) {
	manGen = manifestWebapp
	manName = WebappManifestName

	doUpgrade(1)

	inst, err := NewInstaller(db, fs, &InstallerOptions{
		Operation: Install,
		Type:      Webapp,
		Slug:      "cozy-app-b",
		SourceURL: "git://localhost/",
	})
	if !assert.NoError(t, err) {
		return
	}

	man, err := inst.RunSync()
	assert.NoError(t, err)

	ok, err := afero.Exists(baseFS, path.Join("/", man.Slug(), man.Version(), WebappManifestName))
	assert.NoError(t, err)
	assert.True(t, ok, "The manifest is present")
	ok, err = afero.FileContainsBytes(baseFS, path.Join("/", man.Slug(), man.Version(), WebappManifestName), []byte("1.0.0"))
	assert.NoError(t, err)
	assert.True(t, ok, "The manifest has the right version")
	version1 := man.Version()

	doUpgrade(2)

	inst, err = NewInstaller(db, fs, &InstallerOptions{
		Operation: Update,
		Type:      Webapp,
		Slug:      "cozy-app-b",
	})
	if !assert.NoError(t, err) {
		return
	}

	go inst.Run()

	var state State
	for {
		var done bool
		man, done, err = inst.Poll()
		if !assert.NoError(t, err) {
			return
		}
		if state == "" {
			if !assert.EqualValues(t, Upgrading, man.State()) {
				return
			}
		} else if state == Upgrading {
			if !assert.EqualValues(t, Ready, man.State()) {
				return
			}
			if !assert.True(t, done) {
				return
			}
			break
		} else {
			t.Fatalf("invalid state")
			return
		}
		state = man.State()
	}
	version2 := man.Version()

	fmt.Println("versions:", version1, version2)

	ok, err = afero.Exists(baseFS, path.Join("/", man.Slug(), man.Version(), WebappManifestName))
	assert.NoError(t, err)
	assert.True(t, ok, "The manifest is present")
	ok, err = afero.FileContainsBytes(baseFS, path.Join("/", man.Slug(), man.Version(), WebappManifestName), []byte("2.0.0"))
	assert.NoError(t, err)
	assert.True(t, ok, "The manifest has the right version")
}

func TestWebappInstallAndUpgradeWithBranch(t *testing.T) {
	manGen = manifestWebapp
	manName = WebappManifestName
	doUpgrade(3)

	inst, err := NewInstaller(db, fs, &InstallerOptions{
		Operation: Install,
		Type:      Webapp,
		Slug:      "local-cozy-mini-branch",
		SourceURL: "git://localhost/#branch",
	})
	if !assert.NoError(t, err) {
		return
	}

	go inst.Run()

	var state State
	var man Manifest
	for {
		var done bool
		var err2 error
		man, done, err2 = inst.Poll()
		if !assert.NoError(t, err2) {
			return
		}
		if state == "" {
			if !assert.EqualValues(t, Installing, man.State()) {
				return
			}
		} else if state == Installing {
			if !assert.EqualValues(t, Ready, man.State()) {
				return
			}
			if !assert.True(t, done) {
				return
			}
			break
		} else {
			t.Fatalf("invalid state")
			return
		}
		state = man.State()
	}

	ok, err := afero.Exists(baseFS, path.Join("/", man.Slug(), man.Version(), WebappManifestName))
	assert.NoError(t, err)
	assert.True(t, ok, "The manifest is present")
	ok, err = afero.FileContainsBytes(baseFS, path.Join("/", man.Slug(), man.Version(), WebappManifestName), []byte("3.0.0"))
	assert.NoError(t, err)
	assert.True(t, ok, "The manifest has the right version")
	ok, err = afero.Exists(baseFS, path.Join("/", man.Slug(), man.Version(), "branch"))
	assert.NoError(t, err)
	assert.True(t, ok, "The good branch was checked out")

	doUpgrade(4)

	inst, err = NewInstaller(db, fs, &InstallerOptions{
		Operation: Update,
		Type:      Webapp,
		Slug:      "local-cozy-mini-branch",
	})
	if !assert.NoError(t, err) {
		return
	}

	go inst.Run()

	state = ""
	for {
		var done bool
		var err2 error
		man, done, err2 = inst.Poll()
		if !assert.NoError(t, err2) {
			return
		}
		if state == "" {
			if !assert.EqualValues(t, Upgrading, man.State()) {
				return
			}
		} else if state == Upgrading {
			if !assert.EqualValues(t, Ready, man.State()) {
				return
			}
			if !assert.True(t, done) {
				return
			}
			break
		} else {
			t.Fatalf("invalid state")
			return
		}
		state = man.State()
	}

	ok, err = afero.Exists(baseFS, path.Join("/", man.Slug(), man.Version(), WebappManifestName))
	assert.NoError(t, err)
	assert.True(t, ok, "The manifest is present")
	ok, err = afero.FileContainsBytes(baseFS, path.Join("/", man.Slug(), man.Version(), WebappManifestName), []byte("4.0.0"))
	assert.NoError(t, err)
	assert.True(t, ok, "The manifest has the right version")
	ok, err = afero.Exists(baseFS, path.Join("/", man.Slug(), man.Version(), "branch"))
	assert.NoError(t, err)
	assert.True(t, ok, "The good branch was checked out")

	doUpgrade(5)

	inst, err = NewInstaller(db, fs, &InstallerOptions{
		Operation: Update,
		Type:      Webapp,
		Slug:      "local-cozy-mini-branch",
		SourceURL: "git://localhost/",
	})
	if !assert.NoError(t, err) {
		return
	}

	man, err = inst.RunSync()
	if !assert.NoError(t, err) {
		return
	}
	assert.Equal(t, "git://localhost/", man.Source())

	ok, err = afero.Exists(baseFS, path.Join("/", man.Slug(), man.Version(), WebappManifestName))
	assert.NoError(t, err)
	assert.True(t, ok, "The manifest is present")
	ok, err = afero.FileContainsBytes(baseFS, path.Join("/", man.Slug(), man.Version(), WebappManifestName), []byte("5.0.0"))
	assert.NoError(t, err)
	assert.True(t, ok, "The manifest has the right version")
	ok, err = afero.Exists(baseFS, path.Join("/", man.Slug(), man.Version(), "branch"))
	assert.NoError(t, err)
	assert.False(t, ok, "The good branch was checked out")
}

func TestWebappInstallFromGithub(t *testing.T) {
	manGen = manifestWebapp
	manName = WebappManifestName
	inst, err := NewInstaller(db, fs, &InstallerOptions{
		Operation: Install,
		Type:      Webapp,
		Slug:      "github-cozy-mini",
		SourceURL: "git://github.com/nono/cozy-mini.git",
	})
	if !assert.NoError(t, err) {
		return
	}

	go inst.Run()

	var state State
	for {
		man, done, err := inst.Poll()
		if !assert.NoError(t, err) {
			return
		}
		if state == "" {
			if !assert.EqualValues(t, Installing, man.State()) {
				return
			}
		} else if state == Installing {
			if !assert.EqualValues(t, Ready, man.State()) {
				return
			}
			if !assert.True(t, done) {
				return
			}
			break
		} else {
			t.Fatalf("invalid state")
			return
		}
		state = man.State()
	}
}

func TestWebappInstallFromGitlab(t *testing.T) {
	manGen = manifestWebapp
	manName = WebappManifestName
	inst, err := NewInstaller(db, fs, &InstallerOptions{
		Operation: Install,
		Type:      Webapp,
		Slug:      "gitlab-cozy-mini",
		SourceURL: "git://framagit.org/nono/cozy-mini.git",
	})
	if !assert.NoError(t, err) {
		return
	}

	go inst.Run()

	var state State
	for {
		man, done, err := inst.Poll()
		if !assert.NoError(t, err) {
			return
		}
		if state == "" {
			if !assert.EqualValues(t, Installing, man.State()) {
				return
			}
		} else if state == Installing {
			if !assert.EqualValues(t, Ready, man.State()) {
				return
			}
			if !assert.True(t, done) {
				return
			}
			break
		} else {
			t.Fatalf("invalid state")
			return
		}
		state = man.State()
	}
}

func TestWebappInstallFromHTTP(t *testing.T) {
	manGen = manifestWebapp
	manName = WebappManifestName
	inst, err := NewInstaller(db, fs, &InstallerOptions{
		Operation: Install,
		Type:      Webapp,
		Slug:      "http-cozy-drive",
		SourceURL: "https://github.com/cozy/cozy-drive/archive/71e5cde66f754f986afc7111962ed2dd361bd5e4.tar.gz",
	})
	if !assert.NoError(t, err) {
		return
	}

	go inst.Run()

	var state State
	for {
		man, done, err := inst.Poll()
		if !assert.NoError(t, err) {
			return
		}
		if state == "" {
			if !assert.EqualValues(t, Installing, man.State()) {
				return
			}
		} else if state == Installing {
			if !assert.EqualValues(t, Ready, man.State()) {
				return
			}
			if !assert.True(t, done) {
				return
			}
			break
		} else {
			t.Fatalf("invalid state")
			return
		}
		state = man.State()
	}
}

func TestWebappUninstall(t *testing.T) {
	manGen = manifestWebapp
	manName = WebappManifestName
	inst1, err := NewInstaller(db, fs, &InstallerOptions{
		Operation: Install,
		Type:      Webapp,
		Slug:      "github-cozy-delete",
		SourceURL: "git://localhost/",
	})
	if !assert.NoError(t, err) {
		return
	}
	go inst1.Run()
	for {
		var done bool
		_, done, err = inst1.Poll()
		if !assert.NoError(t, err) {
			return
		}
		if done {
			break
		}
	}
	inst2, err := NewInstaller(db, fs, &InstallerOptions{
		Operation: Delete,
		Type:      Webapp,
		Slug:      "github-cozy-delete",
	})
	if !assert.NoError(t, err) {
		return
	}
	_, err = inst2.RunSync()
	if !assert.NoError(t, err) {
		return
	}
	inst3, err := NewInstaller(db, fs, &InstallerOptions{
		Operation: Delete,
		Type:      Webapp,
		Slug:      "github-cozy-delete",
	})
	assert.Nil(t, inst3)
	assert.Equal(t, ErrNotFound, err)
}
