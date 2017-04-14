package apps

import (
	"path"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
)

func TestKonnectorInstallSuccessful(t *testing.T) {
	manGen = manifestKonnector
	manName = KonnectorManifestName

	doUpgrade(1)

	inst, err := NewInstaller(db, fs, &InstallerOptions{
		Operation: Install,
		Type:      Konnector,
		Slug:      "local-konnector",
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

	ok, err := afero.Exists(baseFS, path.Join("/", man.Slug(), man.Version(), "app.tar"))
	assert.NoError(t, err)
	assert.True(t, ok, "The manifest is present")
	ok, err = afero.FileContainsBytes(baseFS, path.Join("/", man.Slug(), man.Version(), "app.tar"), []byte("1.0.0"))
	assert.NoError(t, err)
	assert.True(t, ok, "The manifest has the right version")

	inst2, err := NewInstaller(db, fs, &InstallerOptions{
		Operation: Install,
		Type:      Konnector,
		Slug:      "local-konnector",
		SourceURL: "git://localhost/",
	})
	assert.Nil(t, inst2)
	assert.Equal(t, ErrAlreadyExists, err)
}

func TestKonnectorUpgradeNotExist(t *testing.T) {
	manGen = manifestKonnector
	manName = KonnectorManifestName
	inst, err := NewInstaller(db, fs, &InstallerOptions{
		Operation: Update,
		Type:      Konnector,
		Slug:      "cozy-konnector-not-exist",
	})
	assert.Nil(t, inst)
	assert.Equal(t, ErrNotFound, err)

	inst, err = NewInstaller(db, fs, &InstallerOptions{
		Operation: Delete,
		Type:      Konnector,
		Slug:      "cozy-konnector-not-exist",
	})
	assert.Nil(t, inst)
	assert.Equal(t, ErrNotFound, err)
}

func TestKonnectorInstallWithUpgrade(t *testing.T) {
	manGen = manifestKonnector
	manName = KonnectorManifestName

	doUpgrade(1)

	inst, err := NewInstaller(db, fs, &InstallerOptions{
		Operation: Install,
		Type:      Konnector,
		Slug:      "cozy-konnector-b",
		SourceURL: "git://localhost/",
	})
	if !assert.NoError(t, err) {
		return
	}

	go inst.Run()

	var man Manifest
	for {
		var done bool
		man, done, err = inst.Poll()
		if !assert.NoError(t, err) {
			return
		}
		if done {
			break
		}
	}

	ok, err := afero.Exists(baseFS, path.Join("/", man.Slug(), man.Version(), "app.tar"))
	assert.NoError(t, err)
	assert.True(t, ok, "The manifest is present")
	ok, err = afero.FileContainsBytes(baseFS, path.Join("/", man.Slug(), man.Version(), "app.tar"), []byte("1.0.0"))
	assert.NoError(t, err)
	assert.True(t, ok, "The manifest has the right version")

	doUpgrade(2)

	inst, err = NewInstaller(db, fs, &InstallerOptions{
		Operation: Update,
		Type:      Konnector,
		Slug:      "cozy-konnector-b",
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

	ok, err = afero.Exists(baseFS, path.Join("/", man.Slug(), man.Version(), "app.tar"))
	assert.NoError(t, err)
	assert.True(t, ok, "The manifest is present")
	ok, err = afero.FileContainsBytes(baseFS, path.Join("/", man.Slug(), man.Version(), "app.tar"), []byte("2.0.0"))
	assert.NoError(t, err)
	assert.True(t, ok, "The manifest has the right version")
}

func TestKonnectorInstallAndUpgradeWithBranch(t *testing.T) {
	manGen = manifestKonnector
	manName = KonnectorManifestName
	doUpgrade(3)

	inst, err := NewInstaller(db, fs, &InstallerOptions{
		Operation: Install,
		Type:      Konnector,
		Slug:      "local-konnector-branch",
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

	ok, err := afero.Exists(baseFS, path.Join("/", man.Slug(), man.Version(), "app.tar"))
	assert.NoError(t, err)
	assert.True(t, ok, "The manifest is present")
	ok, err = afero.FileContainsBytes(baseFS, path.Join("/", man.Slug(), man.Version(), "app.tar"), []byte("3.0.0"))
	assert.NoError(t, err)
	assert.True(t, ok, "The manifest has the right version")

	doUpgrade(4)

	inst, err = NewInstaller(db, fs, &InstallerOptions{
		Operation: Update,
		Type:      Konnector,
		Slug:      "local-konnector-branch",
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

	ok, err = afero.Exists(baseFS, path.Join("/", man.Slug(), man.Version(), "app.tar"))
	assert.NoError(t, err)
	assert.True(t, ok, "The manifest is present")
	ok, err = afero.FileContainsBytes(baseFS, path.Join("/", man.Slug(), man.Version(), "app.tar"), []byte("4.0.0"))
	assert.NoError(t, err)
	assert.True(t, ok, "The manifest has the right version")
}

func TestKonnectorUninstall(t *testing.T) {
	manGen = manifestKonnector
	manName = KonnectorManifestName
	inst1, err := NewInstaller(db, fs, &InstallerOptions{
		Operation: Install,
		Type:      Konnector,
		Slug:      "konnector-delete",
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
		Type:      Konnector,
		Slug:      "konnector-delete",
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
		Type:      Konnector,
		Slug:      "konnector-delete",
	})
	assert.Nil(t, inst3)
	assert.Equal(t, ErrNotFound, err)
}
