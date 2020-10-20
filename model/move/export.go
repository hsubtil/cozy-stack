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
	"net/url"
	"path"
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
func ExportCopyData(w io.Writer, inst *instance.Instance, exportDoc *ExportDoc, archive io.Reader, cursor Cursor) error {
	exportMetadata := cursor.Number() == 0

	zw := zip.NewWriter(w)
	defer func() {
		_ = zw.Close()
	}()

	gr, err := gzip.NewReader(archive)
	if err != nil {
		return err
	}
	now := time.Now()
	tr := tar.NewReader(gr)

	var root *vfs.TreeFile
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeDir {
			continue
		}

		zipFileHdr := &zip.FileHeader{
			Name:     path.Join(ExportDataDir, hdr.Name),
			Method:   zip.Deflate,
			Modified: now,
		}
		zipFileHdr.SetMode(0750)

		isIndexFile := hdr.Typeflag == tar.TypeReg && hdr.Name == "files-index.json"

		if isIndexFile && exportDoc.AcceptDoctype(consts.Files) {
			jsonData, err := ioutil.ReadAll(tr)
			if err != nil {
				return err
			}
			if err := json.NewDecoder(bytes.NewReader(jsonData)).Decode(&root); err != nil {
				return err
			}
			if exportMetadata {
				zipFileWriter, err := zw.CreateHeader(zipFileHdr)
				if err != nil {
					return err
				}
				_, err = io.Copy(zipFileWriter, bytes.NewReader(jsonData))
				if err != nil {
					return err
				}
			}
		} else if exportMetadata {
			zipFileWriter, err := zw.CreateHeader(zipFileHdr)
			if err != nil {
				return err
			}
			_, err = io.Copy(zipFileWriter, tr)
			if err != nil {
				return err
			}
		}

		if isIndexFile && !exportMetadata {
			break
		}
	}

	if err := gr.Close(); err != nil {
		return err
	}
	if !exportDoc.AcceptDoctype(consts.Files) {
		return nil
	}
	if root == nil {
		return ErrExportDoesNotContainIndex
	}

	fs := inst.VFS()
	list, _ := listFilesIndex(root, nil, indexCursor{}, cursor.index,
		exportDoc.PartsSize, exportDoc.PartsSize)
	for _, file := range list {
		dirDoc, fileDoc := file.file.Refine()
		if fileDoc != nil {
			f, err := fs.OpenFile(fileDoc)
			if err != nil {
				return err
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
			zipFileWriter, err := zw.CreateHeader(hdr)
			if err != nil {
				return err
			}
			if file.rangeStart > 0 {
				_, err = f.Seek(file.rangeStart, 0)
				if err != nil {
					return err
				}
			}
			_, err = io.CopyN(zipFileWriter, f, size)
			if err != nil {
				return err
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
				return err
			}
		}
	}

	return nil
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
