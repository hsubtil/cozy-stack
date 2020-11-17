package vfs

import (
	"path"

	"github.com/cozy/cozy-stack/pkg/consts"
)

type filePatherWithCache struct {
	fs    Indexer
	cache map[string]string // dirID -> parentPath
}

// NewFilePatherWithCache creates a FilePather that will cache the calls to
// CouchDB for finding the parent directories.
func NewFilePatherWithCache(fs Indexer) FilePather {
	return &filePatherWithCache{
		fs:    fs,
		cache: make(map[string]string),
	}
}

func (fp *filePatherWithCache) FilePath(doc *FileDoc) (string, error) {
	var parentPath string
	if doc.DirID == consts.RootDirID {
		parentPath = "/"
	} else if doc.DirID == consts.TrashDirID {
		parentPath = TrashDirName
	} else if cachedPath, ok := fp.cache[doc.DirID]; ok {
		parentPath = cachedPath
	} else {
		parent, err := fp.fs.DirByID(doc.DirID)
		if err != nil {
			return "", ErrParentDoesNotExist
		}
		parentPath = parent.Fullpath
		fp.cache[doc.DirID] = parentPath
	}
	return path.Join(parentPath, doc.DocName), nil
}
