package move

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"time"

	"github.com/cozy/cozy-stack/pkg/consts"
	"github.com/cozy/cozy-stack/pkg/couchdb"
	"github.com/cozy/cozy-stack/pkg/couchdb/mango"
	"github.com/cozy/cozy-stack/pkg/crypto"
	"github.com/cozy/cozy-stack/pkg/instance"
	"github.com/cozy/cozy-stack/pkg/realtime"
	"github.com/cozy/cozy-stack/pkg/utils"
	"github.com/cozy/cozy-stack/pkg/vfs"
	"github.com/cozy/cozy-stack/web/jsonapi"
	"github.com/cozy/echo"
)

// ExportDoc is a documents storing the metadata of an export.
type ExportDoc struct {
	DocID             string        `json:"_id,omitempty"`
	DocRev            string        `json:"_rev,omitempty"`
	Domain            string        `json:"domain"`
	Salt              []byte        `json:"salt"`
	IndexFilesCursors []string      `json:"index_file_cursors"`
	State             string        `json:"state"`
	WithDoctypes      []string      `json:"with_doctypes,omitempty"`
	WithoutIndex      bool          `json:"without_index,omitempty"`
	CreatedAt         time.Time     `json:"created_at"`
	ExpiresAt         time.Time     `json:"expires_at"`
	TotalSize         int64         `json:"total_size"`
	CreationDuration  time.Duration `json:"creation_duration"`
	Error             string        `json:"error"`
}

var (
	// ErrExportNotFound is used when a export document could not be found
	ErrExportNotFound = echo.NewHTTPError(http.StatusNotFound, "exports: not found")
	// ErrExportExpired is used when the export document has expired along with
	// its associated data.
	ErrExportExpired = echo.NewHTTPError(http.StatusNotFound, "exports: has expired")
	// ErrMACInvalid is used when the given MAC is not valid.
	ErrMACInvalid = echo.NewHTTPError(http.StatusUnauthorized, "exports: invalid mac")
	// ErrExportConflict is used when an export is already being perfomed.
	ErrExportConflict = echo.NewHTTPError(http.StatusConflict, "export: an archive is already being created")
)

const (
	// ExportStateExporting is the state used when the export document is being
	// created.
	ExportStateExporting = "exporting"
	// ExportStateDone is used when the export document is finished, without
	// error.
	ExportStateDone = "done"
	// ExportStateError is used when the export document is finshed with error.
	ExportStateError = "error"
)

// DocType implements the couchdb.Doc interface
func (e *ExportDoc) DocType() string { return consts.Exports }

// ID implements the couchdb.Doc interface
func (e *ExportDoc) ID() string { return e.DocID }

// Rev implements the couchdb.Doc interface
func (e *ExportDoc) Rev() string { return e.DocRev }

// SetID implements the couchdb.Doc interface
func (e *ExportDoc) SetID(id string) { e.DocID = id }

// SetRev implements the couchdb.Doc interface
func (e *ExportDoc) SetRev(rev string) { e.DocRev = rev }

// Clone implements the couchdb.Doc interface
func (e *ExportDoc) Clone() couchdb.Doc {
	clone := *e

	clone.Salt = make([]byte, len(e.Salt))
	copy(clone.Salt, e.Salt)

	clone.IndexFilesCursors = make([]string, len(e.IndexFilesCursors))
	copy(clone.IndexFilesCursors, e.IndexFilesCursors)

	return &clone
}

// Links implements the jsonapi.Object interface
func (e *ExportDoc) Links() *jsonapi.LinksList { return nil }

// Relationships implements the jsonapi.Object interface
func (e *ExportDoc) Relationships() jsonapi.RelationshipMap { return nil }

// Included implements the jsonapi.Object interface
func (e *ExportDoc) Included() []jsonapi.Object { return nil }

// HasExpired returns whether or not the export document has expired.
func (e *ExportDoc) HasExpired() bool {
	return time.Until(e.ExpiresAt) <= 0
}

var _ jsonapi.Object = &ExportDoc{}

// GenerateAuthMessage generates a MAC authentificating the access to the
// export data.
func (e *ExportDoc) GenerateAuthMessage(i *instance.Instance) []byte {
	mac, err := crypto.EncodeAuthMessage(archiveMACConfig, i.SessionSecret, nil, e.Salt)
	if err != nil {
		panic(fmt.Errorf("could not generate archive auth message: %s", err))
	}
	return mac
}

// VerifyAuthMessage verifies the given MAC to authenticate and grant the
// access to the export data.
func (e *ExportDoc) VerifyAuthMessage(i *instance.Instance, mac []byte) bool {
	_, err := crypto.DecodeAuthMessage(archiveMACConfig, i.SessionSecret, mac, e.Salt)
	return err == nil
}

// GetExport returns an Export document associated with the given instance and
// with the given ID.
func GetExport(inst *instance.Instance, id string) (*ExportDoc, error) {
	var exportDoc ExportDoc
	if err := couchdb.GetDoc(inst, consts.Exports, id, &exportDoc); err != nil {
		if couchdb.IsNotFoundError(err) || couchdb.IsNoDatabaseError(err) {
			return nil, ErrExportNotFound
		}
		return nil, err
	}
	return &exportDoc, nil
}

// GetExports returns the list of exported documents.
func GetExports(domain string) ([]*ExportDoc, error) {
	var docs []*ExportDoc
	req := &couchdb.FindRequest{
		UseIndex: "by-domain",
		Selector: mango.Equal("domain", domain),
		Sort: mango.SortBy{
			{Field: "domain", Direction: mango.Desc},
			{Field: "created_at", Direction: mango.Desc},
		},
		Limit: 256,
	}
	err := couchdb.FindDocs(couchdb.GlobalDB, consts.Exports, req, &docs)
	if err != nil && !couchdb.IsNoDatabaseError(err) {
		return nil, err
	}
	return docs, nil
}

// Export is used to create a tarball with files and photos from an instance
func Export(i *instance.Instance, opts ExportOptions, archiver Archiver) (exportDoc *ExportDoc, err error) {
	salt := crypto.GenerateRandomBytes(16)
	createdAt := time.Now()

	exportDoc = &ExportDoc{
		Domain:       i.Domain,
		Salt:         salt,
		State:        ExportStateExporting,
		CreatedAt:    createdAt,
		WithDoctypes: opts.WithDoctypes,
		WithoutIndex: opts.WithoutIndex,
		TotalSize:    -1,
	}

	// Cleanup previously archived exports.
	{
		var exportedDocs []*ExportDoc
		exportedDocs, err = GetExports(i.Domain)
		if err != nil {
			return
		}
		notRemovedDocs := exportedDocs[:0]
		for _, e := range exportedDocs {
			if e.State == ExportStateExporting && time.Since(e.CreatedAt) < 24*time.Hour {
				return nil, ErrExportConflict
			}
			notRemovedDocs = append(notRemovedDocs, e)
		}
		if len(notRemovedDocs) > 0 {
			archiver.RemoveArchives(notRemovedDocs)
		}
	}

	var size, n int64
	if err = couchdb.CreateDoc(couchdb.GlobalDB, exportDoc); err != nil {
		return
	}
	realtime.GetHub().Publish(&realtime.Event{
		Verb:   realtime.EventCreate,
		Doc:    exportDoc.Clone(),
		OldDoc: nil,
		Domain: i.Domain,
	})
	defer func() {
		newExportDoc := exportDoc.Clone().(*ExportDoc)
		newExportDoc.CreationDuration = time.Since(createdAt)
		if err == nil {
			newExportDoc.State = ExportStateDone
			newExportDoc.TotalSize = size
		} else {
			newExportDoc.State = ExportStateError
			newExportDoc.Error = err.Error()
		}
		if erru := couchdb.UpdateDoc(couchdb.GlobalDB, newExportDoc); err == nil {
			err = erru
		}
		realtime.GetHub().Publish(&realtime.Event{
			Verb:   realtime.EventUpdate,
			Doc:    newExportDoc.Clone(),
			OldDoc: exportDoc.Clone(),
			Domain: i.Domain,
		})
	}()

	out, err := archiver.CreateArchive(exportDoc)
	if err != nil {
		return
	}
	defer func() {
		if errc := out.Close(); err == nil {
			err = errc
		}
	}()

	gw, err := gzip.NewWriterLevel(out, gzip.BestCompression)
	if err != nil {
		return
	}
	tw := tar.NewWriter(gw)

	if n, err = writeInstanceDoc(i, "instance", createdAt, tw, nil); err != nil {
		return
	}
	size += n

	settings, err := i.SettingsDocument()
	if err != nil {
		return
	}
	if n, err = writeDoc("", "settings", settings, createdAt, tw, nil); err != nil {
		return
	}
	size += n

	if !opts.WithoutIndex {
		var root *vfs.TreeFile
		root, err = i.VFS().BuildTree()
		if err != nil {
			return
		}
		n, err = writeDoc("", "files-index", root, createdAt, tw, nil)
		if err != nil {
			return
		}
		size += n

		cursors, _ := splitFilesIndex(root, nil, BucketSize, BucketSize)
		exportDoc.IndexFilesCursors = cursors
	}

	n, err = exportDocs(i, opts.WithDoctypes, createdAt, tw)
	if errc := tw.Close(); err == nil {
		err = errc
	}
	if errc := gw.Close(); err == nil {
		err = errc
	}
	size += n

	return
}

// BucketSize is the default size of a file bucket, to split the index into
// equal-sized parts.
const BucketSize = 35 * 1024 * 1024 // 50 MB

// splitFilesIndex devides the index into equal size bucket of maximum size
// `bucketSize`. Files can be splitted into multiple parts to accomodate the
// bucket size, using a range. It is used to be able to download the files into
// separate chunks.
//
// The method returns a list of cursor into the index tree for each beginning
// of a new bucket. A cursor has the following format:
//
//    ${dirname}/../${filename}-${byterange-start}
func splitFilesIndex(root *vfs.TreeFile, cursors []string, bucketSize, sizeLeft int64) ([]string, int64) {
	if root.FilesChildrenSize > sizeLeft {
		for _, child := range root.FilesChildren {
			size := child.ByteSize
			if size <= sizeLeft {
				sizeLeft -= size
				continue
			}
			size -= sizeLeft
			for size > 0 {
				rangeStart := (child.ByteSize - size)
				cursor := path.Join(root.Fullpath, child.DocName) + ":" + strconv.FormatInt(rangeStart, 10)
				cursors = append(cursors, cursor)
				size -= bucketSize
			}
			sizeLeft = bucketSize + size
		}
	}
	for _, dir := range root.DirsChildren {
		cursors, sizeLeft = splitFilesIndex(dir, cursors, bucketSize, sizeLeft)
	}
	return cursors, sizeLeft
}

func exportDocs(in *instance.Instance, withDoctypes []string, now time.Time, tw *tar.Writer) (size int64, err error) {
	doctypes, err := couchdb.AllDoctypes(in)
	if err != nil {
		return
	}
	for _, doctype := range doctypes {
		if len(withDoctypes) > 0 && !utils.IsInArray(doctype, withDoctypes) {
			continue
		}
		switch doctype {
		case consts.KonnectorLogs, consts.Archives,
			consts.Sessions, consts.OAuthClients, consts.OAuthAccessCodes:
			// ignore these doctypes
		case consts.Sharings, consts.SharingsAnswer, consts.Shared:
			// ignore sharings ? TBD
		case consts.Files, consts.Settings:
			// already written out in a special file
		default:
			dir := url.PathEscape(doctype)
			err = couchdb.ForeachDocs(in, doctype,
				func(id string, doc json.RawMessage) error {
					n, errw := writeMarshaledDoc(dir, id, doc, now, tw, nil)
					if errw == nil {
						size += n
					}
					return errw
				})
			if err != nil {
				return
			}
		}
	}
	return
}

func writeInstanceDoc(in *instance.Instance, name string,
	now time.Time, tw *tar.Writer, records map[string]string) (int64, error) {
	clone := in.Clone().(*instance.Instance)
	clone.PassphraseHash = nil
	clone.PassphraseResetToken = nil
	clone.PassphraseResetTime = nil
	clone.RegisterToken = nil
	clone.SessionSecret = nil
	clone.OAuthSecret = nil
	clone.CLISecret = nil
	clone.SwiftCluster = 0
	return writeDoc("", name, clone, now, tw, records)
}

func writeDoc(dir, name string, data interface{},
	now time.Time, tw *tar.Writer, records map[string]string) (int64, error) {
	doc, err := json.Marshal(data)
	if err != nil {
		return 0, err
	}
	return writeMarshaledDoc(dir, name, doc, now, tw, records)
}

func writeMarshaledDoc(dir, name string, doc json.RawMessage,
	now time.Time, tw *tar.Writer, records map[string]string) (int64, error) {
	hdr := &tar.Header{
		Name:       path.Join(dir, name+".json"),
		Mode:       0640,
		Size:       int64(len(doc)),
		Typeflag:   tar.TypeReg,
		ModTime:    now,
		PAXRecords: records,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return 0, err
	}
	n, err := tw.Write(doc)
	return int64(n), err
}
