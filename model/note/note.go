// Package note is the glue between the prosemirror models, the VFS, redis, the
// hub for realtime, etc.
package note

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/cozy/cozy-stack/model/instance"
	"github.com/cozy/cozy-stack/model/vfs"
	"github.com/cozy/cozy-stack/pkg/consts"
	"github.com/cozy/cozy-stack/pkg/couchdb"
	"github.com/cozy/prosemirror-go/model"
	"github.com/cozy/prosemirror-go/transform"
)

// Document is the note document in memory. It is persisted to the VFS as a
// file, but with a debounce: the intermediate states are saved in Redis.
type Document struct {
	DocID    string          `json:"_id"`
	DocRev   string          `json:"_rev,omitempty"`
	Title    string          `json:"title"`
	DirID    string          `json:"dir_id,omitempty"`
	Revision int             `json:"revision"`
	Schema   json.RawMessage `json:"schema"`
	Content  interface{}     `json:"content,omitempty"`
}

// ID returns the directory qualified identifier
func (d *Document) ID() string { return d.DocID }

// Rev returns the directory revision
func (d *Document) Rev() string { return d.DocRev }

// DocType returns the document type
func (d *Document) DocType() string { return consts.NotesDocuments }

// Clone implements couchdb.Doc
func (d *Document) Clone() couchdb.Doc {
	cloned := *d
	// XXX The schema and the content are supposed to be immutable and, as
	// such, are not cloned.
	return &cloned
}

// SetID changes the directory qualified identifier
func (d *Document) SetID(id string) { d.DocID = id }

// SetRev changes the directory revision
func (d *Document) SetRev(rev string) { d.DocRev = rev }

// Create the file in the VFS for this note.
func (d *Document) Create(inst *instance.Instance) (*vfs.FileDoc, error) {
	lock := inst.NotesLock()
	if err := lock.Lock(); err != nil {
		return nil, err
	}
	defer lock.Unlock()

	d.Revision = 0
	content, err := d.getInitialContent(inst)
	if err != nil {
		return nil, err
	}
	d.Content = content.ToJSON()

	// TODO markdown
	markdown := []byte(content.String())
	fileDoc, err := d.newFileDoc(inst, markdown)
	if err != nil {
		return nil, err
	}
	if err := writeFile(inst.VFS(), fileDoc, nil, markdown); err != nil {
		return nil, err
	}
	return fileDoc, nil
}

func (d *Document) getInitialContent(inst *instance.Instance) (*model.Node, error) {
	var spec model.SchemaSpec
	if err := json.Unmarshal(d.Schema, &spec); err != nil {
		inst.Logger().WithField("nspace", "notes").
			Infof("Cannot read the schema: %s", err)
		return nil, ErrInvalidSchema
	}

	schema, err := model.NewSchema(&spec)
	if err != nil {
		inst.Logger().WithField("nspace", "notes").
			Infof("Cannot instantiate the schema: %s", err)
		return nil, ErrInvalidSchema
	}

	// Create an empty document that matches the schema constraints.
	typ, err := schema.NodeType(schema.Spec.TopNode)
	if err != nil {
		inst.Logger().WithField("nspace", "notes").
			Infof("The schema is invalid: %s", err)
		return nil, ErrInvalidSchema
	}
	node, err := typ.CreateAndFill()
	if err != nil {
		inst.Logger().WithField("nspace", "notes").
			Infof("The topNode cannot be created: %s", err)
		return nil, ErrInvalidSchema
	}
	return node, nil
}

func (d *Document) getDirID(inst *instance.Instance) (string, error) {
	if d.DirID != "" {
		return d.DirID, nil
	}
	parent, err := ensureNotesDir(inst)
	if err != nil {
		return "", err
	}
	return parent.ID(), nil
}

func titleToFilename(title string) string {
	if title == "" {
		title = "New note"
	}
	name := strings.ReplaceAll(title, "/", "-")
	return name + ".cozy-note"
}

func (d *Document) newFileDoc(inst *instance.Instance, content []byte) (*vfs.FileDoc, error) {
	dirID, err := d.getDirID(inst)
	if err != nil {
		return nil, err
	}

	fileDoc, err := vfs.NewFileDoc(
		titleToFilename(d.Title),
		dirID,
		int64(len(content)),
		nil, // Let the VFS compute the md5sum
		"text/markdown",
		"text",
		time.Now(),
		false, // Not executable
		false, // Not trashed
		nil,   // No tags
	)
	if err != nil {
		return nil, err
	}

	fileDoc.Metadata = d.metadata()
	fileDoc.CozyMetadata = vfs.NewCozyMetadata(inst.PageURL("/", nil))
	return fileDoc, nil
}

func (d *Document) metadata() map[string]interface{} {
	return map[string]interface{}{
		"title":    d.Title,
		"content":  d.Content,
		"revision": d.Revision,
		"schema":   d.Schema,
	}
}

// TODO retry if another file with the same name already exists
func writeFile(fs vfs.VFS, fileDoc, oldDoc *vfs.FileDoc, content []byte) (err error) {
	var file vfs.File
	file, err = fs.CreateFile(fileDoc, oldDoc)
	if err != nil {
		return
	}
	defer func() {
		if cerr := file.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()
	_, err = file.Write(content)
	return
}

func ensureNotesDir(inst *instance.Instance) (*vfs.DirDoc, error) {
	ref := couchdb.DocReference{
		Type: consts.Apps,
		ID:   consts.Apps + "/" + consts.NotesSlug,
	}
	key := []string{ref.Type, ref.ID}
	end := []string{ref.Type, ref.ID, couchdb.MaxString}
	req := &couchdb.ViewRequest{
		StartKey:    key,
		EndKey:      end,
		IncludeDocs: true,
	}
	var res couchdb.ViewResponse
	err := couchdb.ExecView(inst, couchdb.FilesReferencedByView, req, &res)
	if err != nil {
		return nil, err
	}

	fs := inst.VFS()
	if len(res.Rows) > 0 {
		return fs.DirByID(res.Rows[0].ID)
	}
	dirname := inst.Translate("Tree Notes")
	dir, err := vfs.NewDirDocWithPath(dirname, consts.RootDirID, "/", nil)
	if err != nil {
		return nil, err
	}
	dir.AddReferencedBy(ref)
	dir.CozyMetadata = vfs.NewCozyMetadata(inst.PageURL("/", nil))
	if err = fs.CreateDir(dir); err != nil {
		if !couchdb.IsConflictError(err) {
			return nil, err
		}
		dir, err = fs.DirByPath(dir.Fullpath)
		if err != nil {
			return nil, err
		}
		olddoc := dir.Clone().(*vfs.DirDoc)
		dir.AddReferencedBy(ref)
		_ = fs.UpdateDirDoc(olddoc, dir)
	}
	return dir, nil
}

// UpdateTitle changes the title of a note and renames the associated file.
// TODO add debounce
func UpdateTitle(inst *instance.Instance, file *vfs.FileDoc, title string) error {
	if len(file.Metadata) == 0 {
		return ErrInvalidFile
	}
	old, _ := file.Metadata["title"].(string)
	if old == title {
		return nil
	}

	olddoc := file.Clone().(*vfs.FileDoc)
	file.Metadata["title"] = title
	file.UpdatedAt = time.Now()
	file.CozyMetadata.UpdatedAt = file.UpdatedAt

	// If the file was renamed manually before, we will keep its name. Else, we
	// can rename with the new title.
	if rename := titleToFilename(old) == file.DocName; rename {
		file.DocName = titleToFilename(title)
		file.ResetFullpath()
	}

	return inst.VFS().UpdateFileDoc(olddoc, file)
}

// ApplySteps takes a note and some steps, and tries to apply them. It is an
// all or nothing change: if there is one error, the note won't be changed.
// TODO fetch last info for file (if debounce)
func ApplySteps(inst *instance.Instance, file *vfs.FileDoc, steps []couchdb.JSONDoc) error {
	if len(steps) == 0 {
		return ErrNoSteps
	}

	oldContent, ok := file.Metadata["content"].(map[string]interface{})
	if !ok {
		return ErrInvalidFile
	}
	schemaSpec, ok := file.Metadata["schema"].(map[string]interface{})
	if !ok {
		return ErrInvalidSchema
	}

	spec := model.SchemaSpecFromJSON(schemaSpec)
	schema, err := model.NewSchema(&spec)
	if err != nil {
		inst.Logger().WithField("nspace", "notes").
			Infof("Cannot instantiate the schema: %s", err)
		return ErrInvalidSchema
	}

	doc, err := model.NodeFromJSON(schema, oldContent)
	if err != nil {
		inst.Logger().WithField("nspace", "notes").
			Infof("Cannot instantiate the document: %s", err)
		return ErrInvalidFile
	}

	for _, s := range steps {
		step, err := transform.StepFromJSON(schema, s.M)
		if err != nil {
			inst.Logger().WithField("nspace", "notes").
				Infof("Cannot instantiate a step: %s", err)
			return ErrInvalidSteps
		}
		result := step.Apply(doc)
		if result.Failed != "" {
			inst.Logger().WithField("nspace", "notes").
				Infof("Cannot apply a step: %s", err)
			return ErrCannotApply
		}
		doc = result.Doc
	}

	olddoc := file.Clone().(*vfs.FileDoc)
	file.Metadata["content"] = doc.ToJSON()
	// TODO markdown
	markdown := []byte(doc.String())

	// TODO add debounce
	return writeFile(inst.VFS(), file, olddoc, markdown)
}

var _ couchdb.Doc = &Document{}
