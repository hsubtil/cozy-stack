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

	file, seq, err := s.findNextFileToUpload(inst, lastSeq)
	if err != nil {
		return false, err
	}
	if file == nil {
		if seq != lastSeq {
			err = s.UpdateLastSequenceNumber(inst, m, "upload", seq)
		}
		return false, err
	}

	if err = s.uploadFile(inst, m, file); err != nil {
		return false, err
	}

	return true, s.UpdateLastSequenceNumber(inst, m, "upload", seq)
}

// findNextFileToUpload uses the changes feed to find the next file that needs
// to be uploaded. It returns a file document if there is one file to upload,
// and the sequence number where it is in the changes feed.
func (s *Sharing) findNextFileToUpload(inst *instance.Instance, since string) (*couchdb.JSONDoc, string, error) {
	for {
		response, err := couchdb.GetChanges(inst, &couchdb.ChangesRequest{
			DocType:     consts.Shared,
			IncludeDocs: true,
			Since:       since,
			Limit:       1,
		})
		if err != nil {
			return nil, since, err
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
		revisions, ok := r.Doc.Get("revisions").([]interface{})
		if !ok {
			continue
		}
		var file couchdb.JSONDoc
		docID := strings.SplitN(r.DocID, "/", 2)[0]
		if err = couchdb.GetDoc(inst, consts.Files, docID, &file); err != nil {
			return nil, since, err
		}
		file.M["_revisions"] = revisions
		return &file, since, nil
	}
	return nil, since, nil
}

// KeyToUpload contains the key for uploading a file (when syncing metadata is
// not enough)
type KeyToUpload struct {
	Key string `json:"key"`
}

func (s *Sharing) createUploadKey(inst *instance.Instance, target *vfs.FileDoc) (*KeyToUpload, error) {
	key := getCache().Save(inst.Domain, target)
	return &KeyToUpload{Key: key}, nil
}

// SyncFile tries to synchroniza a file with just the metadata. If it can't,
// it will return a key to upload the content.
// TODO _revisions is missing in vfs.FileDoc
func (s *Sharing) SyncFile(inst *instance.Instance, target *vfs.FileDoc) (*KeyToUpload, error) {
	if len(target.MD5Sum) == 0 {
		return nil, vfs.ErrInvalidHash
	}
	fs := inst.VFS()
	current, err := fs.FileByID(target.DocID)
	if err != nil {
		if err == os.ErrNotExist {
			return s.createUploadKey(inst, target)
		}
		return nil, err
	}
	var ref SharedRef
	err = couchdb.GetDoc(inst, consts.Shared, consts.Files+"/"+target.DocID, &ref)
	if err != nil {
		if couchdb.IsNotFoundError(err) {
			return nil, ErrInternalServerError // TODO better error for safety principal
		}
		return nil, err
	}
	if !bytes.Equal(target.MD5Sum, current.MD5Sum) {
		return s.createUploadKey(inst, target)
	}
	// TODO sync
	return nil, nil
}

// uploadFile uploads one file to the given member. It first try to just send
// the metadata, and if it is not enough, it also send the binary.
func (s *Sharing) uploadFile(inst *instance.Instance, m *Member, file *couchdb.JSONDoc) error {
	creds := s.FindCredentials(m)
	if creds == nil {
		return ErrInvalidSharing
	}
	u, err := url.Parse(m.Instance)
	if err != nil {
		return err
	}
	origFileID := file.ID()
	// TODO TransformFileToSent
	body, err := json.Marshal(file)
	if err != nil {
		return err
	}

	res, err := request.Req(&request.Options{
		Method: http.MethodPut,
		Scheme: u.Scheme,
		Domain: u.Host,
		Path:   "/sharings/" + s.SID + "/io.cozy.files/" + file.ID() + "/metadata",
		Headers: request.Headers{
			"Accept":        "application/json",
			"Content-Type":  "application/json",
			"Authorization": "Bearer " + creds.AccessToken.AccessToken,
		},
		Body: bytes.NewReader(body),
	})
	if err != nil {
		return err
	}
	if res.StatusCode/100 == 4 {
		res.Body.Close()
		if err = creds.Refresh(inst, s, m); err != nil {
			return err
		}
		res, err = request.Req(&request.Options{
			Method: http.MethodPut,
			Scheme: u.Scheme,
			Domain: u.Host,
			Path:   "/sharings/" + s.SID + "/io.cozy.files/" + file.ID() + "/metadata",
			Headers: request.Headers{
				"Accept":        "application/json",
				"Content-Type":  "application/json",
				"Authorization": "Bearer " + creds.AccessToken.AccessToken,
			},
			Body: bytes.NewReader(body),
		})
		if err != nil {
			return err
		}
	}
	defer res.Body.Close()
	if res.StatusCode/100 == 5 {
		return ErrInternalServerError
	}
	if res.StatusCode/100 != 2 {
		return ErrClientError
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
	if res2.StatusCode/100 == 5 {
		return ErrInternalServerError
	}
	if res2.StatusCode/100 != 2 {
		return ErrClientError
	}
	return nil
}

// HandleFileUpload is used to receive a file upload when synchronizing just
// the metadata was not enough.
func (s *Sharing) HandleFileUpload(inst *instance.Instance, key string, body io.ReadCloser) (err error) {
	newdoc := getCache().Get(inst.Domain, key)
	if newdoc == nil {
		return ErrMissingFileMetadata
	}

	rev := newdoc.Rev()
	// TODO
	// revisions, ok := newdoc["_revisions"].(map[string]interface{})
	// if !ok {
	// 	inst.Logger().WithField("nspace", "replicator").
	// 		Debugf("Missing _revisions for file upload: %#v", target)
	// 	return ErrInternalServerError
	// }
	indexer := NewSharingIndexer(inst, &bulkRevs{
		Rev: rev,
		// Revisions: revisions,
	})
	fs := inst.VFS().UseSharingIndexer(indexer)

	if newdoc.DirID != "" {
		_, err = fs.DirByID(newdoc.DirID)
		// TODO better handling of this conflict
		if err != nil {
			inst.Logger().WithField("nspace", "replicator").
				Debugf("Conflict for parent on file upload: %s", err)
			return err
		}
	} else {
		parent, err := s.GetSharingDir(inst)
		if err != nil {
			return err
		}
		newdoc.DirID = parent.DocID
	}

	olddoc, err := fs.FileByID(newdoc.ID())
	if err != nil && err != os.ErrNotExist {
		return err
	}
	file, err := fs.CreateFile(newdoc, olddoc)
	if err != nil {
		inst.Logger().WithField("nspace", "replicator").
			Debugf("Cannot create file: %s", err)
		return err
	}

	defer func() {
		if cerr := file.Close(); cerr != nil && err == nil {
			err = cerr
			inst.Logger().WithField("nspace", "replicator").
				Debugf("Cannot close file descriptor: %s", err)
		}
	}()
	_, err = io.Copy(file, body)
	// TODO update the io.cozy.shared reference?
	return err
}
