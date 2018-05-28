package sharing

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/cozy/cozy-stack/client/request"
	"github.com/cozy/cozy-stack/pkg/consts"
	"github.com/cozy/cozy-stack/pkg/couchdb"
	"github.com/cozy/cozy-stack/pkg/instance"
	"github.com/cozy/cozy-stack/pkg/lock"
	"github.com/cozy/cozy-stack/pkg/vfs"
	multierror "github.com/hashicorp/go-multierror"
)

// UploadMsg is used for jobs on the share-upload worker.
type UploadMsg struct {
	SharingID string `json:"sharing_id"`
	Errors    int    `json:"errors"`
}

// Upload starts uploading files for this sharing
func (s *Sharing) Upload(inst *instance.Instance, errors int) error {
	mu := lock.ReadWrite(inst.Domain + "/sharings/" + s.SID + "/upload")
	mu.Lock()
	defer mu.Unlock()

	var errm error
	var members []*Member
	if !s.Owner {
		members = append(members, &s.Members[0])
	} else {
		for i, m := range s.Members {
			if i == 0 {
				continue
			}
			if m.Status == MemberStatusReady {
				members = append(members, &s.Members[i])
			}
		}
	}

	// TODO what if we have more than BatchSize files to upload?
	for i := 0; i < BatchSize; i++ {
		if len(members) == 0 {
			break
		}
		m := members[0]
		members = members[1:]
		more, err := s.UploadTo(inst, m)
		if err != nil {
			errm = multierror.Append(errm, err)
		}
		if more {
			members = append(members, m)
		}
	}

	if errm != nil {
		s.retryWorker(inst, "share-upload", errors)
		fmt.Printf("DEBUG errm=%s\n", errm)
	}
	return errm
}

// InitialUpload uploads files to just a member, for the first time
func (s *Sharing) InitialUpload(inst *instance.Instance, m *Member) error {
	mu := lock.ReadWrite(inst.Domain + "/sharings/" + s.SID + "/upload")
	mu.Lock()
	defer mu.Unlock()

	// TODO what if we have more than BatchSize files to upload?
	for i := 0; i < BatchSize; i++ {
		more, err := s.UploadTo(inst, m)
		if err != nil {
			return err
		}
		if !more {
			return nil
		}
	}

	return nil
}

// UploadTo uploads one file to the given member. It returns false if there
// are no more files to upload to this member currently.
func (s *Sharing) UploadTo(inst *instance.Instance, m *Member) (bool, error) {
	if m.Instance == "" {
		return false, ErrInvalidURL
	}
	creds := s.FindCredentials(m)
	if creds == nil {
		return false, ErrInvalidSharing
	}

	lastSeq, err := s.getLastSeqNumber(inst, m, "upload")
	if err != nil {
		return false, err
	}
	inst.Logger().WithField("nspace", "upload").Debugf("lastSeq = %s", lastSeq)

	file, ruleIndex, seq, err := s.findNextFileToUpload(inst, lastSeq)
	if err != nil {
		return false, err
	}
	if file == nil {
		if seq != lastSeq {
			err = s.UpdateLastSequenceNumber(inst, m, "upload", seq)
		}
		return false, err
	}

	if err = s.uploadFile(inst, m, file, ruleIndex); err != nil {
		return false, err
	}

	return true, s.UpdateLastSequenceNumber(inst, m, "upload", seq)
}

// findNextFileToUpload uses the changes feed to find the next file that needs
// to be uploaded. It returns a file document if there is one file to upload,
// and the sequence number where it is in the changes feed.
func (s *Sharing) findNextFileToUpload(inst *instance.Instance, since string) (map[string]interface{}, int, string, error) {
	for {
		response, err := couchdb.GetChanges(inst, &couchdb.ChangesRequest{
			DocType:     consts.Shared,
			IncludeDocs: true,
			Since:       since,
			Limit:       1,
		})
		if err != nil {
			return nil, 0, since, err
		}
		since = response.LastSeq
		if len(response.Results) == 0 {
			break
		}
		r := response.Results[0]
		infos, ok := r.Doc.Get("infos").(map[string]interface{})
		if !ok {
			continue
		}
		info, ok := infos[s.SID].(map[string]interface{})
		if !ok {
			continue
		}
		if _, ok = info["binary"]; !ok {
			continue
		}
		if _, ok = info["removed"]; ok {
			continue
		}
		idx, ok := info["rule"].(float64)
		if !ok {
			continue
		}
		rev := extractLastRevision(r.Doc)
		if rev == "" {
			continue
		}
		docID := strings.SplitN(r.DocID, "/", 2)[1]
		ir := couchdb.IDRev{ID: docID, Rev: rev}
		query := []couchdb.IDRev{ir}
		results, err := couchdb.BulkGetDocs(inst, consts.Files, query)
		if err != nil {
			return nil, 0, since, err
		}
		if len(results) == 0 {
			return nil, 0, since, ErrInternalServerError
		}
		return results[0], int(idx), since, nil
	}
	return nil, 0, since, nil
}

// uploadFile uploads one file to the given member. It first try to just send
// the metadata, and if it is not enough, it also send the binary.
func (s *Sharing) uploadFile(inst *instance.Instance, m *Member, file map[string]interface{}, ruleIndex int) error {
	creds := s.FindCredentials(m)
	if creds == nil {
		return ErrInvalidSharing
	}
	u, err := url.Parse(m.Instance)
	if err != nil {
		return err
	}
	origFileID := file["_id"].(string)
	s.TransformFileToSent(file, creds.XorKey, ruleIndex)
	xoredFileID := file["_id"].(string)
	body, err := json.Marshal(file)
	if err != nil {
		return err
	}
	opts := &request.Options{
		Method: http.MethodPut,
		Scheme: u.Scheme,
		Domain: u.Host,
		Path:   "/sharings/" + s.SID + "/io.cozy.files/" + xoredFileID + "/metadata",
		Headers: request.Headers{
			"Accept":        "application/json",
			"Content-Type":  "application/json",
			"Authorization": "Bearer " + creds.AccessToken.AccessToken,
		},
		Body: bytes.NewReader(body),
	}
	var res *http.Response
	res, err = request.Req(opts)
	if err != nil {
		return err
	}
	if res.StatusCode/100 == 4 {
		res.Body.Close()
		res, err = RefreshToken(inst, s, m, creds, opts, body)
		if err != nil {
			return err
		}
	}
	defer res.Body.Close()
	if res.StatusCode/100 == 5 {
		return ErrInternalServerError
	}

	if res.StatusCode == 204 {
		return nil
	}
	var resBody KeyToUpload
	if err = json.NewDecoder(res.Body).Decode(&resBody); err != nil {
		return err
	}

	fs := inst.VFS()
	fileDoc, err := fs.FileByID(origFileID)
	if err != nil {
		return err
	}
	content, err := fs.OpenFile(fileDoc)
	if err != nil {
		return err
	}
	defer content.Close()

	res2, err := request.Req(&request.Options{
		Method: http.MethodPut,
		Scheme: u.Scheme,
		Domain: u.Host,
		Path:   "/sharings/" + s.SID + "/io.cozy.files/" + resBody.Key,
		Headers: request.Headers{
			"Authorization": "Bearer " + creds.AccessToken.AccessToken,
			"Content-Type":  fileDoc.Mime,
		},
		Body: content,
	})
	if err != nil {
		return err
	}
	res2.Body.Close()
	if res2.StatusCode/100 == 5 {
		return ErrInternalServerError
	}
	if res2.StatusCode/100 != 2 {
		return ErrClientError
	}
	return nil
}

// FileDocWithRevisions is the struct of the payload for synchronizing a file
type FileDocWithRevisions struct {
	*vfs.FileDoc
	Revisions map[string]interface{} `json:"_revisions"`
}

// KeyToUpload contains the key for uploading a file (when syncing metadata is
// not enough)
type KeyToUpload struct {
	Key string `json:"key"`
}

func (s *Sharing) createUploadKey(inst *instance.Instance, target *FileDocWithRevisions) (*KeyToUpload, error) {
	key, err := getStore().Save(inst.Domain, target)
	if err != nil {
		return nil, err
	}
	return &KeyToUpload{Key: key}, nil
}

// SyncFile tries to synchronize a file with just the metadata. If it can't,
// it will return a key to upload the content.
func (s *Sharing) SyncFile(inst *instance.Instance, target *FileDocWithRevisions) (*KeyToUpload, error) {
	if len(target.MD5Sum) == 0 {
		return nil, vfs.ErrInvalidHash
	}
	current, err := inst.VFS().FileByID(target.DocID)
	if err != nil {
		if err == os.ErrNotExist {
			if s.findRuleForNewFile(target.FileDoc) == nil {
				return nil, ErrSafety
			}
			return s.createUploadKey(inst, target)
		}
		return nil, err
	}
	var ref SharedRef
	err = couchdb.GetDoc(inst, consts.Shared, consts.Files+"/"+target.DocID, &ref)
	if err != nil {
		if couchdb.IsNotFoundError(err) {
			return nil, ErrSafety
		}
		return nil, err
	}
	infos, ok := ref.Infos[s.SID]
	if !ok {
		return nil, ErrSafety
	}
	if !bytes.Equal(target.MD5Sum, current.MD5Sum) {
		return s.createUploadKey(inst, target)
	}
	rule := &s.Rules[infos.Rule]
	return nil, s.updateFileMetadata(inst, target, current, rule)
}

// prepareFileWithAncestors find the parent directory for file, and recreates it
// if it is missing.
func (s *Sharing) prepareFileWithAncestors(inst *instance.Instance, newdoc *vfs.FileDoc, dirID string) error {
	if dirID == "" {
		parent, err := s.GetSharingDir(inst)
		if err != nil {
			return err
		}
		newdoc.DirID = parent.DocID
	} else if dirID != newdoc.DirID {
		parent, err := inst.VFS().DirByID(dirID)
		if err == os.ErrNotExist {
			parent, err = s.recreateParent(inst, dirID)
		}
		if err != nil {
			inst.Logger().WithField("nspace", "upload").
				Debugf("Conflict for parent on sync file: %s", err)
			return err
		}
		newdoc.DirID = parent.DocID
	}
	return nil
}

// updateFileMetadata updates a file document when only some metadata has
// changed, but not the content.
func (s *Sharing) updateFileMetadata(inst *instance.Instance, target *FileDocWithRevisions, newdoc *vfs.FileDoc, rule *Rule) error {
	indexer := newSharingIndexer(inst, &bulkRevs{
		Rev:       target.DocRev,
		Revisions: target.Revisions,
	})

	chain := revsStructToChain(target.Revisions)
	conflict := detectConflict(newdoc.DocRev, chain)
	switch conflict {
	case LostConflict:
		return nil
	case WonConflict:
		indexer.WillResolveConflict(newdoc.DocRev, chain)
	case NoConflict:
		// Nothing to do
	}

	fs := inst.VFS().UseSharingIndexer(indexer)
	olddoc := newdoc.Clone().(*vfs.FileDoc)
	newdoc.DocName = target.DocName
	if err := s.prepareFileWithAncestors(inst, newdoc, target.DirID); err != nil {
		return err
	}
	copySafeFieldsToFile(target.FileDoc, newdoc)
	newdoc.ReferencedBy = buildReferencedBy(target.FileDoc, newdoc, rule)

	err := fs.UpdateFileDoc(olddoc, newdoc)
	if err == os.ErrExist {
		pth, errp := newdoc.Path(fs)
		if errp != nil {
			return errp
		}
		name, errr := resolveConflictSamePath(inst, newdoc.DocID, pth)
		if errr != nil {
			return errr
		}
		if name != "" {
			indexer.IncrementRevision()
			newdoc.DocName = name
		}
		err = fs.UpdateFileDoc(olddoc, newdoc)
	}
	if err != nil {
		inst.Logger().WithField("nspace", "upload").
			Debugf("Cannot update file: %s", err)
		return err
	}
	return nil
}

// HandleFileUpload is used to receive a file upload when synchronizing just
// the metadata was not enough.
func (s *Sharing) HandleFileUpload(inst *instance.Instance, key string, body io.ReadCloser) error {
	target, err := getStore().Get(inst.Domain, key)
	inst.Logger().WithField("nspace", "upload").
		Debugf("target = %#v\n", target)
	if err != nil {
		return err
	}
	if target == nil {
		return ErrMissingFileMetadata
	}

	current, err := inst.VFS().FileByID(target.DocID)
	if err != nil && err != os.ErrNotExist {
		inst.Logger().WithField("nspace", "upload").
			Warnf("Upload has failed: %s", err)
		return err
	}

	if current == nil {
		return s.UploadNewFile(inst, target, body)
	}
	return s.UploadExistingFile(inst, target, current, body)
}

// UploadNewFile is used to receive a new file.
func (s *Sharing) UploadNewFile(inst *instance.Instance, target *FileDocWithRevisions, body io.ReadCloser) error {
	indexer := newSharingIndexer(inst, &bulkRevs{
		Rev:       target.Rev(),
		Revisions: target.Revisions,
	})
	fs := inst.VFS().UseSharingIndexer(indexer)

	var err error
	var parent *vfs.DirDoc
	if target.DirID != "" {
		parent, err = fs.DirByID(target.DirID)
		if err == os.ErrNotExist {
			parent, err = s.recreateParent(inst, target.DirID)
		}
		if err != nil {
			inst.Logger().WithField("nspace", "upload").
				Debugf("Conflict for parent on file upload: %s", err)
			return err
		}
	} else {
		parent, err = s.GetSharingDir(inst)
		if err != nil {
			return err
		}
	}

	newdoc, err := vfs.NewFileDoc(
		target.DocName,
		parent.DocID,
		target.Size(),
		target.MD5Sum,
		target.Mime,
		target.Class,
		target.CreatedAt,
		target.Executable,
		false,
		target.Tags)
	if err != nil {
		return err
	}
	newdoc.SetID(target.DocID)
	copySafeFieldsToFile(target.FileDoc, newdoc)

	rule := s.findRuleForNewFile(newdoc)
	if rule == nil {
		return ErrSafety
	}
	newdoc.ReferencedBy = buildReferencedBy(target.FileDoc, nil, rule)

	file, err := fs.CreateFile(newdoc, nil)
	if err == os.ErrExist {
		pth, errp := newdoc.Path(fs)
		if errp != nil {
			return errp
		}
		name, errr := resolveConflictSamePath(inst, newdoc.DocID, pth)
		if errr != nil {
			return errr
		}
		if name != "" {
			indexer.IncrementRevision()
			newdoc.DocName = name
		}
		file, err = fs.CreateFile(newdoc, nil)
	}
	if err != nil {
		inst.Logger().WithField("nspace", "upload").
			Debugf("Cannot create file: %s", err)
		return err
	}
	return s.copyFileContent(inst, file, body)
}

// UploadExistingFile is used to receive new content for an existing file.
//
// Note: if file was renamed + its content has changed, we modify the content
// first, then rename it, not trying to do both at the same time. We do it in
// this order because the difficult case is if one operation succeeds and the
// other fails (if the two suceeds, we are fine; if the two fails, we just
// retry), and in that case, it is easier to manage a conflict on dir_id+name
// than on content: a conflict on different content is resolved by a copy of
// the file (which is not what we want), a conflict of name+dir_id, the higher
// revision wins and it should be the good one in our case.
func (s *Sharing) UploadExistingFile(inst *instance.Instance, target *FileDocWithRevisions, newdoc *vfs.FileDoc, body io.ReadCloser) error {
	indexer := newSharingIndexer(inst, &bulkRevs{
		Rev:       target.Rev(),
		Revisions: target.Revisions,
	})
	fs := inst.VFS().UseSharingIndexer(indexer)
	olddoc := newdoc.Clone().(*vfs.FileDoc)

	chain := revsStructToChain(target.Revisions)
	conflict := detectConflict(newdoc.DocRev, chain)
	if conflict == LostConflict {
		// TODO create a new file from body?
		return nil
	}

	var ref SharedRef
	err := couchdb.GetDoc(inst, consts.Shared, consts.Files+"/"+target.DocID, &ref)
	if err != nil {
		if couchdb.IsNotFoundError(err) {
			return ErrSafety
		}
		return err
	}
	infos, ok := ref.Infos[s.SID]
	if !ok {
		return ErrSafety
	}
	rule := &s.Rules[infos.Rule]
	newdoc.ReferencedBy = buildReferencedBy(target.FileDoc, olddoc, rule)
	copySafeFieldsToFile(target.FileDoc, newdoc)
	s.prepareFileWithAncestors(inst, newdoc, target.DirID)

	if conflict == WonConflict {
		// TODO create a new file from olddoc
	}

	if newdoc.DocName == olddoc.DocName && newdoc.DirID == olddoc.DirID {
		if conflict == WonConflict {
			indexer.WillResolveConflict(target.DocRev, chain)
		}
		file, err := fs.CreateFile(newdoc, olddoc)
		if err != nil {
			return err
		}
		return s.copyFileContent(inst, file, body)
	}

	// TODO Can we use a revision generated by CouchDB for the first operation
	// (content modified), and not forcing a revision? If we can remove this
	// revision after the renaming, it should be fine. Else, there is a risk
	// that it can be seen as a conflict
	tmpdoc := newdoc.Clone().(*vfs.FileDoc)
	tmpdoc.DocName = olddoc.DocName
	tmpdoc.DirID = olddoc.DirID
	file, err := fs.CreateFile(tmpdoc, olddoc)
	if err != nil {
		return err
	}
	if err = s.copyFileContent(inst, file, body); err != nil {
		return err
	}

	err = fs.UpdateFileDoc(tmpdoc, newdoc)
	if err == os.ErrExist {
		pth, errp := newdoc.Path(fs)
		if errp != nil {
			return errp
		}
		name, errr := resolveConflictSamePath(inst, newdoc.DocID, pth)
		if errr != nil {
			return errr
		}
		if name != "" {
			indexer.IncrementRevision()
			newdoc.DocName = name
		}
		err = fs.UpdateFileDoc(tmpdoc, newdoc)
	}
	return err
}

// copyFileContent will copy the body of the HTTP request to the file, and
// close the file descriptor at the end.
func (s *Sharing) copyFileContent(inst *instance.Instance, file vfs.File, body io.ReadCloser) error {
	_, err := io.Copy(file, body)
	if cerr := file.Close(); cerr != nil && err == nil {
		err = cerr
		inst.Logger().WithField("nspace", "upload").
			Debugf("Cannot close file descriptor: %s", err)
	}
	return err
}
