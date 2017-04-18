package vfs

import (
	"os"
	"path"
	"strings"
	"time"

	"github.com/cozy/cozy-stack/pkg/consts"
	"github.com/cozy/cozy-stack/pkg/couchdb"
)

// DirDoc is a struct containing all the informations about a
// directory. It implements the couchdb.Doc and jsonapi.Object
// interfaces.
type DirDoc struct {
	// Type of document. Useful to (de)serialize and filter the data
	// from couch.
	Type string `json:"type"`
	// Qualified file identifier
	DocID string `json:"_id,omitempty"`
	// Directory revision
	DocRev string `json:"_rev,omitempty"`
	// Directory name
	DocName string `json:"name"`
	// Parent directory identifier
	DirID       string `json:"dir_id"`
	RestorePath string `json:"restore_path,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Tags      []string  `json:"tags"`

	// Directory path on VFS
	Fullpath string `json:"path"`
}

// ID returns the directory qualified identifier
func (d *DirDoc) ID() string { return d.DocID }

// Rev returns the directory revision
func (d *DirDoc) Rev() string { return d.DocRev }

// DocType returns the directory document type
func (d *DirDoc) DocType() string { return consts.Files }

// Clone implements couchdb.Doc
func (d *DirDoc) Clone() couchdb.Doc { cloned := *d; return &cloned }

// SetID changes the directory qualified identifier
func (d *DirDoc) SetID(id string) { d.DocID = id }

// SetRev changes the directory revision
func (d *DirDoc) SetRev(rev string) { d.DocRev = rev }

// Path is used to generate the file path
func (d *DirDoc) Path(fs VFS) (string, error) {
	return d.Fullpath, nil
}

// Parent returns the parent directory document
func (d *DirDoc) Parent(fs VFS) (*DirDoc, error) {
	parent, err := fs.DirByID(d.DirID)
	if os.IsNotExist(err) {
		err = ErrParentDoesNotExist
	}
	return parent, err
}

// Name returns base name of the file
func (d *DirDoc) Name() string { return d.DocName }

// Size returns the length in bytes for regular files; system-dependent for others
func (d *DirDoc) Size() int64 { return 0 }

// Mode returns the file mode bits
func (d *DirDoc) Mode() os.FileMode { return 0755 }

// ModTime returns the modification time
func (d *DirDoc) ModTime() time.Time { return d.UpdatedAt }

// IsDir returns the abbreviation for Mode().IsDir()
func (d *DirDoc) IsDir() bool { return true }

// Sys returns the underlying data source (can return nil)
func (d *DirDoc) Sys() interface{} { return nil }

// IsEmpty returns whether or not the directory has at least one child.
func (d *DirDoc) IsEmpty(fs VFS) (bool, error) {
	iter := fs.DirIterator(d, &IteratorOptions{ByFetch: 1})
	_, _, err := iter.Next()
	if err == ErrIteratorDone {
		return true, nil
	}
	return false, err
}

// NewDirDoc is the DirDoc constructor. The given name is validated.
func NewDirDoc(index Indexer, name, dirID string, tags []string) (*DirDoc, error) {
	if err := checkFileName(name); err != nil {
		return nil, err
	}

	if dirID == "" {
		dirID = consts.RootDirID
	}

	var dirPath string
	if dirID == consts.RootDirID {
		dirPath = "/"
	} else {
		parent, err := index.DirByID(dirID)
		if err != nil {
			return nil, err
		}
		dirPath = parent.Fullpath
	}

	return NewDirDocWithPath(name, dirID, dirPath, tags)
}

// NewDirDocWithParent returns an instance of DirDoc from a parent document.
// The given name is validated.
func NewDirDocWithParent(name string, parent *DirDoc, tags []string) (*DirDoc, error) {
	if err := checkFileName(name); err != nil {
		return nil, err
	}

	createDate := time.Now()
	return &DirDoc{
		Type:    consts.DirType,
		DocName: name,
		DirID:   parent.DocID,

		CreatedAt: createDate,
		UpdatedAt: createDate,
		Tags:      uniqueTags(tags),
		Fullpath:  path.Join(parent.Fullpath, name),
	}, nil
}

// NewDirDocWithPath returns an instance of DirDoc its directory ID and path.
// The given name is validated.
func NewDirDocWithPath(name, dirID, dirPath string, tags []string) (*DirDoc, error) {
	if err := checkFileName(name); err != nil {
		return nil, err
	}

	createDate := time.Now()
	return &DirDoc{
		Type:    consts.DirType,
		DocName: name,
		DirID:   dirID,

		CreatedAt: createDate,
		UpdatedAt: createDate,
		Tags:      uniqueTags(tags),
		Fullpath:  path.Join(dirPath, name),
	}, nil
}

// ModifyDirMetadata modify the metadata associated to a directory. It
// can be used to rename or move the directory in the VFS.
func ModifyDirMetadata(fs VFS, olddoc *DirDoc, patch *DocPatch) (*DirDoc, error) {
	id := olddoc.ID()
	if id == consts.RootDirID || id == consts.TrashDirID {
		return nil, os.ErrInvalid
	}

	var err error
	cdate := olddoc.CreatedAt
	patch, err = normalizeDocPatch(&DocPatch{
		Name:        &olddoc.DocName,
		DirID:       &olddoc.DirID,
		RestorePath: &olddoc.RestorePath,
		Tags:        &olddoc.Tags,
		UpdatedAt:   &olddoc.UpdatedAt,
	}, patch, cdate)

	if err != nil {
		return nil, err
	}

	var newdoc *DirDoc
	if *patch.DirID != olddoc.DirID {
		newdoc, err = NewDirDoc(fs, *patch.Name, *patch.DirID, *patch.Tags)
	} else {
		newdoc, err = NewDirDocWithPath(*patch.Name, olddoc.DirID, path.Dir(olddoc.Fullpath), *patch.Tags)
	}
	if err != nil {
		return nil, err
	}

	newdoc.RestorePath = *patch.RestorePath
	newdoc.CreatedAt = cdate
	newdoc.UpdatedAt = *patch.UpdatedAt

	if err = fs.UpdateDirDoc(olddoc, newdoc); err != nil {
		return nil, err
	}
	return newdoc, nil
}

func setTrashedForFilesInsideDir(fs VFS, doc *DirDoc, trashed bool) error {
	files := []*FileDoc{}
	err := walk(fs, doc.Name(), doc, nil, func(name string, dir *DirDoc, file *FileDoc, err error) error {
		if file != nil {
			file.Trashed = trashed
			files = append(files, file)
		}
		return err
	})
	if err != nil {
		return err
	}
	return fs.UpdateFileDocs(files)
}

// TrashDir is used to delete a directory given its document
func TrashDir(fs VFS, olddoc *DirDoc) (*DirDoc, error) {
	oldpath, err := olddoc.Path(fs)
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(oldpath, TrashDirName) {
		return nil, ErrFileInTrash
	}

	trashDirID := consts.TrashDirID
	restorePath := path.Dir(oldpath)

	if err = setTrashedForFilesInsideDir(fs, olddoc, true); err != nil {
		return nil, err
	}

	var newdoc *DirDoc
	tryOrUseSuffix(olddoc.DocName, conflictFormat, func(name string) error {
		newdoc, err = ModifyDirMetadata(fs, olddoc, &DocPatch{
			DirID:       &trashDirID,
			RestorePath: &restorePath,
			Name:        &name,
		})
		return err
	})
	if err != nil {
		return nil, err
	}

	return newdoc, nil
}

// RestoreDir is used to restore a trashed directory given its document
func RestoreDir(fs VFS, olddoc *DirDoc) (*DirDoc, error) {
	oldpath, err := olddoc.Path(fs)
	if err != nil {
		return nil, err
	}
	restoreDir, err := getRestoreDir(fs, oldpath, olddoc.RestorePath)
	if err != nil {
		return nil, err
	}

	var newdoc *DirDoc
	var emptyStr string
	name := stripSuffix(olddoc.DocName, conflictSuffix)
	tryOrUseSuffix(name, "%s (%s)", func(name string) error {
		newdoc, err = ModifyDirMetadata(fs, olddoc, &DocPatch{
			DirID:       &restoreDir.DocID,
			RestorePath: &emptyStr,
			Name:        &name,
		})
		return err
	})
	if err != nil {
		return nil, err
	}

	if err := setTrashedForFilesInsideDir(fs, olddoc, false); err != nil {
		return nil, err
	}

	return newdoc, nil
}

var (
	_ couchdb.Doc = &DirDoc{}
	_ os.FileInfo = &DirDoc{}
)
