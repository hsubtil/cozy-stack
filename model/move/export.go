package move

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/cozy/cozy-stack/model/instance"
	"github.com/cozy/cozy-stack/model/note"
	"github.com/cozy/cozy-stack/model/vfs"
	"github.com/cozy/cozy-stack/pkg/consts"
	"github.com/cozy/cozy-stack/pkg/couchdb"
	"github.com/cozy/cozy-stack/pkg/realtime"
)

// ExportOptions contains the options for launching the export worker.
type ExportOptions struct {
	PartsSize        int64         `json:"parts_size"`
	MaxAge           time.Duration `json:"max_age"`
	WithDoctypes     []string      `json:"with_doctypes,omitempty"`
	ContextualDomain string        `json:"contextual_domain,omitempty"`
}

// minimalPartsSize is the minimal size of a file bucket, to split the index
// into equal-sized parts.
const minimalPartsSize = 1024 * 1024 * 1024 // 1 GB

const (
	// ExportDataDir is the directory for storing the documents from CouchDB in
	// the export archive.
	ExportDataDir = "My Cozy/Data"
	// ExportFilesDir is the directory for storing the content of the files in
	// the export archive.
	ExportFilesDir = "My Cozy/Files"
	// ExportVersionsDir is the directory for storing the content of the old
	// versions of the files in the export archive.
	ExportVersionsDir = "My Cozy/Versions"
)

// ExportCopyData does an HTTP copy of a part of the file indexes.
func ExportCopyData(w http.ResponseWriter, inst *instance.Instance, archiver Archiver, mac []byte, cursorStr string) (err error) {
	exportDoc, err := GetExport(inst, mac)
	if err != nil {
		return err
	}
	if exportDoc.HasExpired() {
		return ErrExportExpired
	}

	partNumber := 0
	// check that the given cursor is part of our pre-defined list of cursors.
	if cursorStr != "" {
		for i, c := range exportDoc.PartsCursors {
			if c == cursorStr {
				partNumber = i + 1
				break
			}
		}
		if partNumber == 0 {
			return ErrExportInvalidCursor
		}
	} else if exportDoc.AcceptDoctype(consts.Files) {
		return ErrExportDoesNotContainIndex
	}

	exportMetadata := partNumber == 0
	cursor, err := parseCursor(cursorStr)
	if err != nil {
		return ErrExportInvalidCursor
	}

	archive, err := archiver.OpenArchive(inst, exportDoc)
	if err != nil {
		return err
	}

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=cozy-export.part%03d.zip", partNumber))
	w.WriteHeader(http.StatusOK)

	zw := zip.NewWriter(w)
	defer func() {
		if errc := zw.Close(); err == nil {
			err = errc
		}
	}()

	var root *vfs.TreeFile
	gr, err := gzip.NewReader(archive)
	if err != nil {
		return err
	}

	now := time.Now()
	tr := tar.NewReader(gr)
	for {
		var hdr *tar.Header
		hdr, err = tr.Next()
		if err == io.EOF {
			err = nil
			break
		}
		if err != nil {
			return
		}
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeDir {
			continue
		}

		var zipFileWriter io.Writer
		zipFileHdr := &zip.FileHeader{
			Name:     path.Join(ExportDataDir, hdr.Name),
			Method:   zip.Deflate,
			Modified: now,
		}
		zipFileHdr.SetMode(0750)

		isIndexFile := hdr.Typeflag == tar.TypeReg && hdr.Name == "files-index.json"

		if isIndexFile && exportDoc.AcceptDoctype(consts.Files) {
			var jsonData []byte
			jsonData, err = ioutil.ReadAll(tr)
			if err != nil {
				return
			}
			if err = json.NewDecoder(bytes.NewReader(jsonData)).Decode(&root); err != nil {
				return
			}
			if exportMetadata {
				zipFileWriter, err = zw.CreateHeader(zipFileHdr)
				if err != nil {
					return
				}
				_, err = io.Copy(zipFileWriter, bytes.NewReader(jsonData))
				if err != nil {
					return
				}
			}
		} else if exportMetadata {
			zipFileWriter, err = zw.CreateHeader(zipFileHdr)
			if err != nil {
				return
			}
			_, err = io.Copy(zipFileWriter, tr)
			if err != nil {
				return
			}
		}

		if isIndexFile && !exportMetadata {
			break
		}
	}

	if errc := gr.Close(); err == nil {
		err = errc
	}
	if errc := archive.Close(); err == nil {
		err = errc
	}
	if err != nil || !exportDoc.AcceptDoctype(consts.Files) {
		return
	}

	if root == nil {
		return ErrExportDoesNotContainIndex
	}

	fs := inst.VFS()
	list, _ := listFilesIndex(root, nil, indexCursor{}, cursor,
		exportDoc.PartsSize, exportDoc.PartsSize)
	for _, file := range list {
		dirDoc, fileDoc := file.file.Refine()
		if fileDoc != nil {
			var f vfs.File
			f, err = fs.OpenFile(fileDoc)
			if err != nil {
				return
			}
			size := file.rangeEnd - file.rangeStart
			hdr := &zip.FileHeader{
				Name:     path.Join(ExportFilesDir, file.file.Fullpath),
				Method:   zip.Deflate,
				Modified: fileDoc.UpdatedAt,
			}
			if fileDoc.Executable {
				hdr.SetMode(0750)
			} else {
				hdr.SetMode(0640)
			}
			if size < file.file.ByteSize {
				hdr.Name += fmt.Sprintf(".range%d-%d", file.rangeStart, file.rangeEnd)
			}
			var zipFileWriter io.Writer
			zipFileWriter, err = zw.CreateHeader(hdr)
			if err != nil {
				return
			}
			if file.rangeStart > 0 {
				_, err = f.Seek(file.rangeStart, 0)
				if err != nil {
					return
				}
			}
			_, err = io.CopyN(zipFileWriter, f, size)
			if err != nil {
				return
			}
		} else {
			hdr := &zip.FileHeader{
				Name:     path.Join(ExportFilesDir, dirDoc.Fullpath) + "/",
				Method:   zip.Deflate,
				Modified: dirDoc.UpdatedAt,
			}
			hdr.SetMode(0750)
			_, err = zw.CreateHeader(hdr)
			if err != nil {
				return
			}
		}
	}

	return
}

// CreateExport is used to create a tarball with the data from an instance.
//
// Note: the tarball is a .tar.gz and not a .zip to allow streaming from Swift
// to the stack, and from the stack to the client, as .tar.gz can be read
// sequentially and reading a .zip need to seek.
func CreateExport(i *instance.Instance, opts ExportOptions, archiver Archiver) (exportDoc *ExportDoc, err error) {
	exportDoc = prepareExportDoc(i, opts)
	createdAt := exportDoc.CreatedAt
	if err = exportDoc.CleanPreviousExports(archiver); err != nil {
		return
	}

	var size, n int64
	if err = couchdb.CreateDoc(couchdb.GlobalDB, exportDoc); err != nil {
		return
	}
	realtime.GetHub().Publish(i, realtime.EventCreate, exportDoc.Clone(), nil)
	defer func() {
		old := exportDoc.Clone()
		if erru := exportDoc.MarksAsFinished(i, size, err); erru == nil {
			realtime.GetHub().Publish(i, realtime.EventUpdate, exportDoc, old)
		} else if err == nil {
			err = erru
		}
	}()

	var out io.WriteCloser
	out, err = archiver.CreateArchive(exportDoc)
	if err != nil {
		return
	}
	defer func() {
		if errc := out.Close(); err == nil {
			err = errc
		}
	}()

	var gw io.WriteCloser
	gw, err = gzip.NewWriterLevel(out, gzip.BestCompression)
	if err != nil {
		return
	}
	tw := tar.NewWriter(gw)
	defer func() {
		if errc := tw.Close(); err == nil {
			err = errc
		}
		if errc := gw.Close(); err == nil {
			err = errc
		}
	}()

	if n, err = writeInstanceDoc(i, "instance", createdAt, tw); err != nil {
		return
	}
	size += n

	if exportDoc.AcceptDoctype(consts.Files) {
		_ = note.FlushPendings(i)
		var tree *vfs.Tree
		tree, err = i.VFS().BuildTree()
		if err != nil {
			return
		}
		n, err = writeDoc("", "files-index", tree.Root, createdAt, tw)
		if err != nil {
			return
		}
		size += n

		exportDoc.PartsCursors, _ = splitFilesIndex(tree.Root, nil, nil, exportDoc.PartsSize, exportDoc.PartsSize)
	}

	n, err = exportDocuments(i, exportDoc, createdAt, tw)
	if err == nil {
		size += n
	}
	return
}

// splitFilesIndex devides the index into equal size bucket of maximum size
// `bucketSize`. Files can be splitted into multiple parts to accommodate the
// bucket size, using a range. It is used to be able to download the files into
// separate chunks.
//
// The method returns a list of cursor into the index tree for each beginning
// of a new bucket. A cursor has the following format:
//
//    ${dirname}/../${filename}-${byterange-start}
func splitFilesIndex(root *vfs.TreeFile, cursor []string, cursors []string, bucketSize, sizeLeft int64) ([]string, int64) {
	for childIndex, child := range root.FilesChildren {
		size := child.ByteSize
		if size <= sizeLeft {
			sizeLeft -= size
			continue
		}
		size -= sizeLeft
		for size > 0 {
			rangeStart := (child.ByteSize - size)
			cursorStr := strings.Join(append(cursor, strconv.Itoa(childIndex)), "/")
			cursorStr += ":" + strconv.FormatInt(rangeStart, 10)
			cursorStr = "/" + cursorStr
			cursors = append(cursors, cursorStr)
			size -= bucketSize
		}
		sizeLeft = -size
	}
	for dirIndex, dir := range root.DirsChildren {
		cursors, sizeLeft = splitFilesIndex(dir, append(cursor, strconv.Itoa(dirIndex)),
			cursors, bucketSize, sizeLeft)
	}
	return cursors, sizeLeft
}

type fileRanged struct {
	file       *vfs.TreeFile
	rangeStart int64
	rangeEnd   int64
}

// listFilesIndex browse the index with the given cursor and returns the
// flatting list of file entering the bucket.
func listFilesIndex(root *vfs.TreeFile, list []fileRanged, currentCursor, cursor indexCursor, bucketSize, sizeLeft int64) ([]fileRanged, int64) {
	if sizeLeft < 0 {
		return list, sizeLeft
	}

	cursorDiff := cursor.diff(currentCursor)
	cursorEqual := cursorDiff == 0 && currentCursor.equal(cursor)

	if cursorDiff >= 0 {
		for childIndex, child := range root.FilesChildren {
			var fileRangeStart, fileRangeEnd int64
			if cursorEqual {
				if childIndex < cursor.fileCursor {
					continue
				} else if childIndex == cursor.fileCursor {
					fileRangeStart = cursor.fileRangeStart
				}
			}
			if sizeLeft <= 0 {
				return list, sizeLeft
			}
			size := child.ByteSize - fileRangeStart
			if sizeLeft-size < 0 {
				fileRangeEnd = fileRangeStart + sizeLeft
			} else {
				fileRangeEnd = child.ByteSize
			}
			list = append(list, fileRanged{child, fileRangeStart, fileRangeEnd})
			sizeLeft -= size
			if sizeLeft < 0 {
				return list, sizeLeft
			}
		}

		// append empty directory so that we explicitly create them in the tarball
		if len(root.DirsChildren) == 0 && len(root.FilesChildren) == 0 {
			list = append(list, fileRanged{root, 0, 0})
		}
	}

	for dirIndex, dir := range root.DirsChildren {
		list, sizeLeft = listFilesIndex(dir, list, currentCursor.next(dirIndex),
			cursor, bucketSize, sizeLeft)
	}

	return list, sizeLeft
}

type indexCursor struct {
	dirCursor      []int
	fileCursor     int
	fileRangeStart int64
}

func (c indexCursor) diff(d indexCursor) int {
	l := len(d.dirCursor)
	if len(c.dirCursor) < l {
		l = len(c.dirCursor)
	}
	for i := 0; i < l; i++ {
		if diff := d.dirCursor[i] - c.dirCursor[i]; diff != 0 {
			return diff
		}
	}
	if len(d.dirCursor) > len(c.dirCursor) {
		return 1
	} else if len(d.dirCursor) < len(c.dirCursor) {
		return -1
	}
	return 0
}

func (c indexCursor) equal(d indexCursor) bool {
	l := len(d.dirCursor)
	if l != len(c.dirCursor) {
		return false
	}
	for i := 0; i < l; i++ {
		if d.dirCursor[i] != c.dirCursor[i] {
			return false
		}
	}
	return true
}

func (c indexCursor) next(dirIndex int) (next indexCursor) {
	next.dirCursor = append(c.dirCursor, dirIndex)
	next.fileCursor = 0
	next.fileRangeStart = 0
	return
}

func parseCursor(cursor string) (c indexCursor, err error) {
	if cursor == "" {
		return
	}
	ss := strings.Split(cursor, "/")
	if len(ss) < 2 {
		err = ErrExportInvalidCursor
		return
	}
	if ss[0] != "" {
		err = ErrExportInvalidCursor
		return
	}
	ss = ss[1:]
	c.dirCursor = make([]int, len(ss)-1)
	for i, s := range ss {
		if i == len(ss)-1 {
			rangeSplit := strings.SplitN(s, ":", 2)
			if len(rangeSplit) != 2 {
				err = ErrExportInvalidCursor
				return
			}
			c.fileCursor, err = strconv.Atoi(rangeSplit[0])
			if err != nil {
				return
			}
			c.fileRangeStart, err = strconv.ParseInt(rangeSplit[1], 10, 64)
			if err != nil {
				return
			}
		} else {
			c.dirCursor[i], err = strconv.Atoi(s)
			if err != nil {
				return
			}
		}
	}
	return
}

func exportDocuments(in *instance.Instance, doc *ExportDoc, now time.Time, tw *tar.Writer) (int64, error) {
	doctypes, err := couchdb.AllDoctypes(in)
	if err != nil {
		return 0, err
	}

	var size int64
	for _, doctype := range doctypes {
		if !doc.AcceptDoctype(doctype) {
			continue
		}
		switch doctype {
		case consts.Files, consts.FilesVersions:
			// we have code specific to those doctypes
			continue
		}
		dir := url.PathEscape(doctype)
		err := couchdb.ForeachDocs(in, doctype, func(id string, doc json.RawMessage) error {
			n, err := writeMarshaledDoc(dir, id, doc, now, tw)
			if err == nil {
				size += n
			}
			return err
		})
		if err != nil {
			return 0, err
		}
	}
	return size, nil
}

func writeInstanceDoc(in *instance.Instance, name string, now time.Time, tw *tar.Writer) (int64, error) {
	clone := in.Clone().(*instance.Instance)
	clone.PassphraseHash = nil
	clone.PassphraseResetToken = nil
	clone.PassphraseResetTime = nil
	clone.RegisterToken = nil
	clone.SessSecret = nil
	clone.OAuthSecret = nil
	clone.CLISecret = nil
	clone.SwiftLayout = 0
	clone.IndexViewsVersion = 0
	return writeDoc("", name, clone, now, tw)
}

func writeDoc(dir, name string, data interface{}, now time.Time, tw *tar.Writer) (int64, error) {
	doc, err := json.Marshal(data)
	if err != nil {
		return 0, err
	}
	return writeMarshaledDoc(dir, name, doc, now, tw)
}

func writeMarshaledDoc(dir, name string, doc json.RawMessage, now time.Time, tw *tar.Writer) (int64, error) {
	hdr := &tar.Header{
		Name:     path.Join(dir, name+".json"),
		Mode:     0640,
		Size:     int64(len(doc)),
		Typeflag: tar.TypeReg,
		ModTime:  now,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return 0, err
	}
	n, err := tw.Write(doc)
	return int64(n), err
}
