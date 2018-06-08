package vfsafero

// #nosec
import (
	"bytes"
	"crypto/md5"
	"fmt"
	"hash"
	"io"
	"net/url"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/cozy/cozy-stack/pkg/couchdb"
	"github.com/cozy/cozy-stack/pkg/lock"
	"github.com/cozy/cozy-stack/pkg/magic"
	"github.com/cozy/cozy-stack/pkg/prefixer"
	"github.com/cozy/cozy-stack/pkg/vfs"

	"github.com/cozy/afero"
)

// aferoVFS is a struct implementing the vfs.VFS interface associated with
// an afero.Fs filesystem. The indexing of the elements of the filesystem is
// done in couchdb.
type aferoVFS struct {
	vfs.Indexer
	vfs.DiskThresholder

	domain string
	prefix string
	fs     afero.Fs
	mu     lock.ErrorRWLocker
	pth    string

	// whether or not the localfilesystem requires an initialisation of its root
	// directory
	osFS bool
}

// New returns a vfs.VFS instance associated with the specified indexer and
// storage url.
//
// The supported scheme of the storage url are file://, for an OS-FS store, and
// mem:// for an in-memory store. The backend used is the afero package.
func New(db prefixer.Prefixer, index vfs.Indexer, disk vfs.DiskThresholder, mu lock.ErrorRWLocker, fsURL *url.URL, pathSegment string) (vfs.VFS, error) {
	if fsURL.Scheme != "mem" && fsURL.Path == "" {
		return nil, fmt.Errorf("vfsafero: please check the supplied fs url: %s",
			fsURL.String())
	}
	if pathSegment == "" {
		return nil, fmt.Errorf("vfsafero: specified path segment is empty")
	}
	pth := path.Join(fsURL.Path, pathSegment)
	var fs afero.Fs
	switch fsURL.Scheme {
	case "file":
		fs = afero.NewBasePathFs(afero.NewOsFs(), pth)
	case "mem":
		fs = afero.NewMemMapFs()
	default:
		return nil, fmt.Errorf("vfsafero: non supported scheme %s", fsURL.Scheme)
	}
	return &aferoVFS{
		Indexer:         index,
		DiskThresholder: disk,

		domain: db.DomainName(),
		prefix: db.DBPrefix(),
		fs:     fs,
		mu:     mu,
		pth:    pth,
		// for now, only the file:// scheme needs a specific initialisation of its
		// root directory.
		osFS: fsURL.Scheme == "file",
	}, nil
}

func (afs *aferoVFS) DomainName() string {
	return afs.domain
}

func (afs *aferoVFS) DBPrefix() string {
	return afs.prefix
}

func (afs *aferoVFS) UseSharingIndexer(index vfs.Indexer) vfs.VFS {
	return &aferoVFS{
		Indexer:         index,
		DiskThresholder: afs.DiskThresholder,
		domain:          afs.domain,
		fs:              afs.fs,
		mu:              afs.mu,
		pth:             afs.pth,
		osFS:            afs.osFS,
	}
}

// Init creates the root directory document and the trash directory for this
// file system.
func (afs *aferoVFS) InitFs() error {
	if lockerr := afs.mu.Lock(); lockerr != nil {
		return lockerr
	}
	defer afs.mu.Unlock()
	if err := afs.Indexer.InitIndex(); err != nil {
		return err
	}
	// for a file:// fs, we need to create the root directory container
	if afs.osFS {
		if err := afero.NewOsFs().MkdirAll(afs.pth, 0755); err != nil {
			return err
		}
	}
	if err := afs.fs.Mkdir(vfs.TrashDirName, 0755); err != nil && !os.IsExist(err) {
		return err
	}
	return nil
}

// Delete removes all the elements associated with the filesystem.
func (afs *aferoVFS) Delete() error {
	if lockerr := afs.mu.Lock(); lockerr != nil {
		return lockerr
	}
	defer afs.mu.Unlock()
	if afs.osFS {
		return afero.NewOsFs().RemoveAll(afs.pth)
	}
	return nil
}

func (afs *aferoVFS) CreateDir(doc *vfs.DirDoc) error {
	if lockerr := afs.mu.Lock(); lockerr != nil {
		return lockerr
	}
	defer afs.mu.Unlock()
	err := afs.fs.Mkdir(doc.Fullpath, 0755)
	if err != nil {
		return err
	}
	if doc.ID() == "" {
		err = afs.Indexer.CreateDirDoc(doc)
	} else {
		err = afs.Indexer.CreateNamedDirDoc(doc)
	}
	if err != nil {
		afs.fs.Remove(doc.Fullpath) // #nosec
	}
	return err
}

func (afs *aferoVFS) CreateFile(newdoc, olddoc *vfs.FileDoc) (vfs.File, error) {
	if lockerr := afs.mu.Lock(); lockerr != nil {
		return nil, lockerr
	}
	defer afs.mu.Unlock()

	diskQuota := afs.DiskQuota()

	var maxsize, newsize, capsize int64
	newsize = newdoc.ByteSize
	if diskQuota > 0 {
		diskUsage, err := afs.DiskUsage()
		if err != nil {
			return nil, err
		}

		var oldsize int64
		if olddoc != nil {
			oldsize = olddoc.Size()
		}
		maxsize = diskQuota - diskUsage
		if maxsize <= 0 || (newsize >= 0 && (newsize-oldsize) > maxsize) {
			return nil, vfs.ErrFileTooBig
		}

		if quotaBytes := int64(9.0 / 10.0 * float64(diskQuota)); diskUsage <= quotaBytes {
			capsize = quotaBytes - diskUsage
		}
	} else {
		maxsize = -1 // no limit
	}

	newpath, err := afs.Indexer.FilePath(newdoc)
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(newpath, vfs.TrashDirName+"/") {
		return nil, vfs.ErrParentInTrash
	}

	tmppath := newpath
	if olddoc != nil {
		tmppath = fmt.Sprintf("/.%s_%s", olddoc.ID(), olddoc.Rev())
	}

	if olddoc != nil {
		newdoc.SetID(olddoc.ID())
		newdoc.SetRev(olddoc.Rev())
		newdoc.CreatedAt = olddoc.CreatedAt
	}

	// Avoid storing negative size in the index.
	if newdoc.ByteSize < 0 {
		newdoc.ByteSize = 0
	}

	if olddoc == nil {
		var exists bool
		exists, err = afs.Indexer.DirChildExists(newdoc.DirID, newdoc.DocName)
		if err != nil {
			return nil, err
		}
		if exists {
			return nil, os.ErrExist
		}

		// When added to the index, the document is first considered hidden. This
		// flag will only be removed at the end of the upload when all its metadata
		// are known. See the Close() method.
		newdoc.Trashed = true

		if newdoc.ID() == "" {
			err = afs.Indexer.CreateFileDoc(newdoc)
		} else {
			err = afs.Indexer.CreateNamedFileDoc(newdoc)
		}
		if err != nil {
			return nil, err
		}
	}

	f, err := safeCreateFile(tmppath, newdoc.Mode(), afs.fs)
	if err != nil {
		return nil, err
	}

	hash := md5.New() // #nosec
	extractor := vfs.NewMetaExtractor(newdoc)

	return &aferoFileCreation{
		w:    0,
		f:    f,
		size: newsize,

		afs:     afs,
		newdoc:  newdoc,
		olddoc:  olddoc,
		tmppath: tmppath,
		newpath: newpath,
		maxsize: maxsize,
		capsize: capsize,

		hash: hash,
		meta: extractor,
	}, nil
}

func (afs *aferoVFS) DestroyDirContent(doc *vfs.DirDoc) error {
	if lockerr := afs.mu.Lock(); lockerr != nil {
		return lockerr
	}
	defer afs.mu.Unlock()
	diskUsage, _ := afs.DiskUsage()
	destroyed, _, err := afs.Indexer.DeleteDirDocAndContent(doc, true)
	if err != nil {
		return err
	}
	vfs.DiskQuotaAfterDestroy(afs, diskUsage, destroyed)
	infos, err := afero.ReadDir(afs.fs, doc.Fullpath)
	if err != nil {
		return err
	}
	for _, info := range infos {
		fullpath := path.Join(doc.Fullpath, info.Name())
		if info.IsDir() {
			err = afs.fs.RemoveAll(fullpath)
		} else {
			err = afs.fs.Remove(fullpath)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (afs *aferoVFS) DestroyDirAndContent(doc *vfs.DirDoc) error {
	if lockerr := afs.mu.Lock(); lockerr != nil {
		return lockerr
	}
	defer afs.mu.Unlock()
	diskUsage, _ := afs.DiskUsage()
	destroyed, _, err := afs.Indexer.DeleteDirDocAndContent(doc, false)
	if err != nil {
		return err
	}
	vfs.DiskQuotaAfterDestroy(afs, diskUsage, destroyed)
	return afs.fs.RemoveAll(doc.Fullpath)
}

func (afs *aferoVFS) DestroyFile(doc *vfs.FileDoc) error {
	if lockerr := afs.mu.Lock(); lockerr != nil {
		return lockerr
	}
	defer afs.mu.Unlock()
	diskUsage, _ := afs.DiskUsage()
	name, err := afs.Indexer.FilePath(doc)
	if err != nil {
		return err
	}
	vfs.DiskQuotaAfterDestroy(afs, diskUsage, doc.ByteSize)
	err = afs.fs.Remove(name)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return afs.Indexer.DeleteFileDoc(doc)
}

func (afs *aferoVFS) OpenFile(doc *vfs.FileDoc) (vfs.File, error) {
	if lockerr := afs.mu.RLock(); lockerr != nil {
		return nil, lockerr
	}
	defer afs.mu.RUnlock()
	name, err := afs.Indexer.FilePath(doc)
	if err != nil {
		return nil, err
	}
	f, err := afs.fs.Open(name)
	if err != nil {
		return nil, err
	}
	return &aferoFileOpen{f}, nil
}

func (afs *aferoVFS) Fsck(opts vfs.FsckOptions) (logbook []*vfs.FsckLog, err error) {
	if lockerr := afs.mu.Lock(); lockerr != nil {
		return nil, lockerr
	}
	defer afs.mu.Unlock()
	logbook, err = afs.Indexer.CheckIndexIntegrity()
	if err != nil {
		return
	}
	if opts.Prune {
		afs.fsckPrune(logbook, opts.DryRun)
	}
	root, err := afs.Indexer.DirByPath("/")
	if err != nil {
		return nil, err
	}
	var newLogs []*vfs.FsckLog
	newLogs, err = afs.fsckWalk(root, newLogs)
	if err != nil {
		return nil, err
	}
	sort.Slice(newLogs, func(i, j int) bool {
		return newLogs[i].Filename < newLogs[j].Filename
	})
	logbook = append(logbook, newLogs...)
	if opts.Prune {
		afs.fsckPrune(newLogs, opts.DryRun)
	}
	return logbook, nil
}

func (afs *aferoVFS) fsckWalk(dir *vfs.DirDoc, logbook []*vfs.FsckLog) ([]*vfs.FsckLog, error) {
	entries := make(map[string]struct{})
	iter := afs.Indexer.DirIterator(dir, nil)
	for {
		d, f, err := iter.Next()
		if err == vfs.ErrIteratorDone {
			break
		}
		if err != nil {
			return nil, err
		}
		var fullpath string
		if f != nil {
			var stat os.FileInfo
			entries[f.DocName] = struct{}{}
			fullpath = path.Join(dir.Fullpath, f.DocName)
			stat, err = afs.fs.Stat(fullpath)
			if _, ok := err.(*os.PathError); ok {
				logbook = append(logbook, &vfs.FsckLog{
					Type:     vfs.FileMissing,
					IsFile:   true,
					FileDoc:  f,
					Filename: fullpath,
				})
			} else if err != nil {
				return nil, err
			} else if stat.IsDir() {
				var dirDoc *vfs.DirDoc
				dirDoc, err = vfs.NewDirDocWithParent(f.DocName, dir, nil)
				if err != nil {
					return nil, err
				}
				logbook = append(logbook, &vfs.FsckLog{
					Type:     vfs.TypeMismatch,
					IsFile:   true,
					DirDoc:   dirDoc,
					FileDoc:  f,
					Filename: fullpath,
				})
			}
		} else {
			entries[d.DocName] = struct{}{}
			var stat os.FileInfo
			stat, err = afs.fs.Stat(d.Fullpath)
			if _, ok := err.(*os.PathError); ok {
				logbook = append(logbook, &vfs.FsckLog{
					Type:     vfs.FileMissing,
					IsFile:   false,
					DirDoc:   d,
					Filename: d.Fullpath,
				})
			} else if err != nil {
				return nil, err
			} else if !stat.IsDir() {
				var fileDoc *vfs.FileDoc
				fileDoc, err = fileInfosToFileDoc(dir, d.Fullpath, stat)
				if err != nil {
					return nil, err
				}
				logbook = append(logbook, &vfs.FsckLog{
					Type:     vfs.TypeMismatch,
					IsFile:   false,
					FileDoc:  fileDoc,
					DirDoc:   d,
					Filename: d.Fullpath,
				})
			} else {
				if logbook, err = afs.fsckWalk(d, logbook); err != nil {
					return nil, err
				}
			}
		}
	}

	fileinfos, err := afero.ReadDir(afs.fs, dir.Fullpath)
	if err != nil {
		return nil, err
	}

	for _, fileinfo := range fileinfos {
		if _, ok := entries[fileinfo.Name()]; !ok {
			filename := path.Join(dir.Fullpath, fileinfo.Name())
			if filename == vfs.WebappsDirName ||
				filename == vfs.KonnectorsDirName ||
				filename == vfs.ThumbsDirName {
				continue
			}
			if fileinfo.Size() == 0 {
				continue
			}
			fileDoc, err := fileInfosToFileDoc(dir, filename, fileinfo)
			if err != nil {
				continue
			}
			logbook = append(logbook, &vfs.FsckLog{
				Type:     vfs.IndexMissing,
				IsFile:   true,
				FileDoc:  fileDoc,
				Filename: filename,
			})
		}
	}

	return logbook, nil
}

func fileInfosToFileDoc(dir *vfs.DirDoc, fullpath string, fileinfo os.FileInfo) (*vfs.FileDoc, error) {
	trashed := strings.HasPrefix(fullpath, vfs.TrashDirName)
	contentType, md5sum, err := extractContentTypeAndMD5(fullpath)
	if err != nil {
		return nil, err
	}
	mime, class := vfs.ExtractMimeAndClass(contentType)
	return vfs.NewFileDoc(
		fileinfo.Name(),
		dir.DocID,
		fileinfo.Size(),
		md5sum,
		mime,
		class,
		fileinfo.ModTime(),
		false,
		trashed,
		nil)
}

// fsckPrune tries to fix the given list on inconsistencies in the VFS
func (afs *aferoVFS) fsckPrune(logbook []*vfs.FsckLog, dryrun bool) {
	for _, entry := range logbook {
		switch entry.Type {
		case vfs.IndexOrphanTree, vfs.IndexBadFullpath, vfs.FileMissing, vfs.IndexMissing:
			vfs.FsckPrune(afs, afs.Indexer, entry, dryrun)
		case vfs.TypeMismatch:
			if entry.IsFile {
				// file on couchdb and directory on swift: we update the index to
				// remove the file index and create a directory one
				err := afs.Indexer.DeleteFileDoc(entry.FileDoc)
				if err != nil {
					entry.PruneError = err
				}
				err = afs.Indexer.CreateDirDoc(entry.DirDoc)
				if err != nil {
					entry.PruneError = err
				}
			} else {
				// directory on couchdb and file on filesystem: we keep the directory
				// and move the object into the orphan directory and create a new index
				// associated with it.
				orphanDir, err := vfs.Mkdir(afs, vfs.OrphansDirName, nil)
				if err != nil {
					entry.PruneError = err
					continue
				}
				oldname := entry.Filename
				newname := path.Join(vfs.OrphansDirName, entry.FileDoc.Name())
				err = afs.fs.Rename(oldname, newname)
				if err != nil {
					entry.PruneError = err
					continue
				}
				err = afs.fs.Mkdir(oldname, 0755)
				if err != nil {
					entry.PruneError = err
					continue
				}
				newdoc := entry.FileDoc.Clone().(*vfs.FileDoc)
				newdoc.DirID = orphanDir.ID()
				newdoc.ResetFullpath()
				err = afs.Indexer.CreateFileDoc(newdoc)
				if err != nil {
					entry.PruneError = err
					continue
				}
			}
		}
	}
}

// UpdateFileDoc overrides the indexer's one since the afero.Fs is by essence
// also indexed by path. When moving a file, the index has to be moved and the
// filesystem should also be updated.
//
// @override Indexer.UpdateFileDoc
func (afs *aferoVFS) UpdateFileDoc(olddoc, newdoc *vfs.FileDoc) error {
	if lockerr := afs.mu.Lock(); lockerr != nil {
		return lockerr
	}
	defer afs.mu.Unlock()
	if newdoc.DirID != olddoc.DirID || newdoc.DocName != olddoc.DocName {
		oldpath, err := afs.Indexer.FilePath(olddoc)
		if err != nil {
			return err
		}
		newpath, err := afs.Indexer.FilePath(newdoc)
		if err != nil {
			return err
		}
		err = safeRenameFile(afs.fs, oldpath, newpath)
		if err != nil {
			return err
		}
	}
	if newdoc.Executable != olddoc.Executable {
		newpath, err := afs.Indexer.FilePath(newdoc)
		if err != nil {
			return err
		}
		err = afs.fs.Chmod(newpath, newdoc.Mode())
		if err != nil {
			return err
		}
	}
	return afs.Indexer.UpdateFileDoc(olddoc, newdoc)
}

// UpdateDirDoc overrides the indexer's one since the afero.Fs is by essence
// also indexed by path. When moving a file, the index has to be moved and the
// filesystem should also be updated.
//
// @override Indexer.UpdateDirDoc
func (afs *aferoVFS) UpdateDirDoc(olddoc, newdoc *vfs.DirDoc) error {
	if lockerr := afs.mu.Lock(); lockerr != nil {
		return lockerr
	}
	defer afs.mu.Unlock()
	if newdoc.Fullpath != olddoc.Fullpath {
		if err := safeRenameDir(afs, olddoc.Fullpath, newdoc.Fullpath); err != nil {
			return err
		}
	}
	return afs.Indexer.UpdateDirDoc(olddoc, newdoc)
}

func (afs *aferoVFS) DirByID(fileID string) (*vfs.DirDoc, error) {
	if lockerr := afs.mu.RLock(); lockerr != nil {
		return nil, lockerr
	}
	defer afs.mu.RUnlock()
	return afs.Indexer.DirByID(fileID)
}

func (afs *aferoVFS) DirByPath(name string) (*vfs.DirDoc, error) {
	if lockerr := afs.mu.RLock(); lockerr != nil {
		return nil, lockerr
	}
	defer afs.mu.RUnlock()
	return afs.Indexer.DirByPath(name)
}

func (afs *aferoVFS) FileByID(fileID string) (*vfs.FileDoc, error) {
	if lockerr := afs.mu.RLock(); lockerr != nil {
		return nil, lockerr
	}
	defer afs.mu.RUnlock()
	return afs.Indexer.FileByID(fileID)
}

func (afs *aferoVFS) FileByPath(name string) (*vfs.FileDoc, error) {
	if lockerr := afs.mu.RLock(); lockerr != nil {
		return nil, lockerr
	}
	defer afs.mu.RUnlock()
	return afs.Indexer.FileByPath(name)
}

func (afs *aferoVFS) FilePath(doc *vfs.FileDoc) (string, error) {
	if lockerr := afs.mu.RLock(); lockerr != nil {
		return "", lockerr
	}
	defer afs.mu.RUnlock()
	return afs.Indexer.FilePath(doc)
}

func (afs *aferoVFS) DirOrFileByID(fileID string) (*vfs.DirDoc, *vfs.FileDoc, error) {
	if lockerr := afs.mu.RLock(); lockerr != nil {
		return nil, nil, lockerr
	}
	defer afs.mu.RUnlock()
	return afs.Indexer.DirOrFileByID(fileID)
}

func (afs *aferoVFS) DirOrFileByPath(name string) (*vfs.DirDoc, *vfs.FileDoc, error) {
	if lockerr := afs.mu.RLock(); lockerr != nil {
		return nil, nil, lockerr
	}
	defer afs.mu.RUnlock()
	return afs.Indexer.DirOrFileByPath(name)
}

// aferoFileOpen represents a file handle opened for reading.
type aferoFileOpen struct {
	f afero.File
}

func (f *aferoFileOpen) Read(p []byte) (int, error) {
	return f.f.Read(p)
}

func (f *aferoFileOpen) ReadAt(p []byte, off int64) (int, error) {
	return f.f.ReadAt(p, off)
}

func (f *aferoFileOpen) Seek(offset int64, whence int) (int64, error) {
	return f.f.Seek(offset, whence)
}

func (f *aferoFileOpen) Write(p []byte) (int, error) {
	return 0, os.ErrInvalid
}

func (f *aferoFileOpen) Close() error {
	return f.f.Close()
}

// aferoFileCreation represents a file open for writing. It is used to
// create of file or to modify the content of a file.
//
// aferoFileCreation implements io.WriteCloser.
type aferoFileCreation struct {
	f       afero.File         // file handle
	w       int64              // total size written
	size    int64              // total file size, -1 if unknown
	afs     *aferoVFS          // parent vfs
	newdoc  *vfs.FileDoc       // new document
	olddoc  *vfs.FileDoc       // old document
	newpath string             // file new path
	tmppath string             // temporary file path for uploading a new version of this file
	maxsize int64              // maximum size allowed for the file
	capsize int64              // size cap from which we send a notification to the user
	hash    hash.Hash          // hash we build up along the file
	meta    *vfs.MetaExtractor // extracts metadata from the content
	err     error              // write error
}

func (f *aferoFileCreation) Read(p []byte) (int, error) {
	return 0, os.ErrInvalid
}

func (f *aferoFileCreation) ReadAt(p []byte, off int64) (int, error) {
	return 0, os.ErrInvalid
}

func (f *aferoFileCreation) Seek(offset int64, whence int) (int64, error) {
	return 0, os.ErrInvalid
}

func (f *aferoFileCreation) Write(p []byte) (int, error) {
	n, err := f.f.Write(p)
	if err != nil {
		f.err = err
		return n, err
	}

	f.w += int64(n)
	if f.maxsize >= 0 && f.w > f.maxsize {
		f.err = vfs.ErrFileTooBig
		return n, f.err
	}

	if f.size >= 0 && f.w > f.size {
		f.err = vfs.ErrContentLengthMismatch
		return n, f.err
	}

	if f.meta != nil {
		if _, err = (*f.meta).Write(p); err != nil && err != io.ErrClosedPipe {
			(*f.meta).Abort(err)
			f.meta = nil
		}
	}

	_, err = f.hash.Write(p)
	return n, err
}

func (f *aferoFileCreation) Close() (err error) {
	defer func() {
		if err == nil {
			if f.olddoc != nil {
				// move the temporary file to its final location
				f.afs.fs.Rename(f.tmppath, f.newpath) // #nosec
			}
			if f.capsize > 0 && f.size >= f.capsize {
				vfs.PushDiskQuotaAlert(f.afs, true)
			}
		} else if err != nil {
			// remove the temporary file if an error occured
			f.afs.fs.Remove(f.tmppath) // #nosec
			// If an error has occured that is not due to the index update, we should
			// delete the file from the index.
			if f.olddoc == nil {
				if _, isCouchErr := couchdb.IsCouchError(err); !isCouchErr {
					f.afs.Indexer.DeleteFileDoc(f.newdoc) // #nosec
				}
			}
		}
	}()

	if err = f.f.Close(); err != nil {
		if f.meta != nil {
			(*f.meta).Abort(err)
		}
		if f.err == nil {
			f.err = err
		}
	}

	newdoc, olddoc, written := f.newdoc, f.olddoc, f.w

	if f.meta != nil {
		if errc := (*f.meta).Close(); errc == nil {
			newdoc.Metadata = (*f.meta).Result()
		}
	}

	if f.err != nil {
		return f.err
	}

	md5sum := f.hash.Sum(nil)
	if newdoc.MD5Sum == nil {
		newdoc.MD5Sum = md5sum
	}

	if !bytes.Equal(newdoc.MD5Sum, md5sum) {
		return vfs.ErrInvalidHash
	}

	if newdoc.ByteSize <= 0 {
		newdoc.ByteSize = written
	}

	if newdoc.ByteSize != written {
		return vfs.ErrContentLengthMismatch
	}

	// The document is already added to the index when closing the file creation
	// handler. When updating the content of the document with the final
	// informations (size, md5, ...) we can reuse the same document as olddoc.
	if olddoc == nil || !olddoc.Trashed {
		newdoc.Trashed = false
	}
	if olddoc == nil {
		olddoc = newdoc.Clone().(*vfs.FileDoc)
	}
	lockerr := f.afs.mu.Lock()
	if lockerr != nil {
		return lockerr
	}
	defer f.afs.mu.Unlock()
	return f.afs.Indexer.UpdateFileDoc(olddoc, newdoc)
}

func safeCreateFile(name string, mode os.FileMode, fs afero.Fs) (afero.File, error) {
	// write only (O_WRONLY), try to create the file and check that it
	// does not already exist (O_CREATE|O_EXCL).
	flag := os.O_WRONLY | os.O_CREATE | os.O_EXCL
	return fs.OpenFile(name, flag, mode)
}

func safeRenameFile(fs afero.Fs, oldpath, newpath string) error {
	newpath = path.Clean(newpath)
	oldpath = path.Clean(oldpath)

	if !path.IsAbs(newpath) || !path.IsAbs(oldpath) {
		return vfs.ErrNonAbsolutePath
	}

	_, err := fs.Stat(newpath)
	if err == nil {
		return os.ErrExist
	}
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	return fs.Rename(oldpath, newpath)
}

func safeRenameDir(afs *aferoVFS, oldpath, newpath string) error {
	newpath = path.Clean(newpath)
	oldpath = path.Clean(oldpath)

	if !path.IsAbs(newpath) || !path.IsAbs(oldpath) {
		return vfs.ErrNonAbsolutePath
	}

	if strings.HasPrefix(newpath, oldpath+"/") {
		return vfs.ErrForbiddenDocMove
	}

	_, err := afs.fs.Stat(newpath)
	if err == nil {
		return os.ErrExist
	}
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	return afs.fs.Rename(oldpath, newpath)
}

func extractContentTypeAndMD5(filename string) (contentType string, md5sum []byte, err error) {
	f, err := os.Open(filename)
	if err != nil {
		return
	}
	defer f.Close()
	var r io.Reader
	contentType, r = magic.MIMETypeFromReader(f)
	h := md5.New() // #nosec
	if _, err = io.Copy(h, r); err != nil {
		return
	}
	md5sum = h.Sum(nil)
	return
}

var (
	_ vfs.VFS  = &aferoVFS{}
	_ vfs.File = &aferoFileOpen{}
	_ vfs.File = &aferoFileCreation{}
)
