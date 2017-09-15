package imexport

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"github.com/cozy/cozy-stack/pkg/couchdb"
	"github.com/cozy/cozy-stack/pkg/instance"
	"github.com/cozy/cozy-stack/pkg/vfs"
)

const (
	metaDir    = "metadata"
	contactExt = ".vcf"
)

func createAlbum(fs vfs.VFS, hdr *tar.Header, tr *tar.Reader, dstDoc *vfs.DirDoc, db couchdb.Database) error {
	m := make(map[string]*couchdb.DocReference)

	bs := bufio.NewScanner(tr)

	for bs.Scan() {
		jsondoc := &couchdb.JSONDoc{}
		err := jsondoc.UnmarshalJSON(bs.Bytes())
		if err != nil {
			return err
		}
		doctype, ok := jsondoc.M["type"].(string)
		if ok {
			jsondoc.Type = doctype
		}
		delete(jsondoc.M, "type")

		id := jsondoc.ID()
		jsondoc.SetID("")
		jsondoc.SetRev("")

		err = couchdb.CreateDoc(db, jsondoc)
		if err != nil {
			return err
		}

		m[id] = &couchdb.DocReference{
			ID:   jsondoc.ID(),
			Type: jsondoc.DocType(),
		}

	}

	hdr, err := tr.Next()
	if err != nil {
		return err
	}
	fmt.Println("end album.json and ", hdr)
	bs = bufio.NewScanner(tr)
	for bs.Scan() {
		ref := &References{}
		err := json.Unmarshal(bs.Bytes(), &ref)
		if err != nil {
			return err
		}

		file, err := fs.FileByPath(dstDoc.Fullpath + ref.Filepath)
		if err != nil {
			return err
		}

		if m[ref.Albumid] != nil {
			file.AddReferencedBy(*m[ref.Albumid])
			if err = couchdb.UpdateDoc(db, file); err != nil {
				return err
			}
		}

	}

	return nil

}

func createFile(fs vfs.VFS, hdr *tar.Header, tr *tar.Reader, dstDoc *vfs.DirDoc) error {
	name := path.Base(hdr.Name)
	mime, class := vfs.ExtractMimeAndClassFromFilename(hdr.Name)
	now := time.Now()
	executable := hdr.FileInfo().Mode()&0100 != 0

	dirDoc, err := fs.DirByPath(path.Join(dstDoc.Fullpath, path.Dir(hdr.Name)))
	if err != nil {
		return err
	}

	fileDoc, err := vfs.NewFileDoc(name, dirDoc.ID(), hdr.Size, nil, mime, class, now, executable, false, nil)
	if err != nil {
		return err
	}

	file, err := fs.CreateFile(fileDoc, nil)
	if err != nil {
		if strings.Contains(path.Dir(hdr.Name), "/Photos/") {
			return nil
		}
		extension := path.Ext(fileDoc.DocName)
		fileName := fileDoc.DocName[0 : len(fileDoc.DocName)-len(extension)]
		fileDoc.DocName = fmt.Sprintf("%s-conflict-%d%s", fileName, time.Now().Unix(), extension)
		file, err = fs.CreateFile(fileDoc, nil)
		if err != nil {
			return err
		}

	}

	_, err = io.Copy(file, tr)
	cerr := file.Close()
	if err != nil {
		return err
	}
	if cerr != nil {
		return cerr
	}

	return nil
}

func createContact(fs vfs.VFS, hdr *tar.Header, tr *tar.Reader, db couchdb.Database) error {

	return nil
}

// Untardir untar doc directory
func Untardir(r io.Reader, dst string, instance *instance.Instance) error {
	fs := instance.VFS()
	domain := instance.Domain
	db := couchdb.SimpleDatabasePrefix(domain)

	dstDoc, err := fs.DirByID(dst)
	if err != nil {
		return err
	}

	//gzip reader
	gr, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	defer gr.Close()

	//tar reader
	tr := tar.NewReader(gr)

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		doc := path.Join(dstDoc.Fullpath, hdr.Name)

		switch hdr.Typeflag {

		case tar.TypeDir:
			fmt.Println(hdr.Name)
			if !strings.Contains(hdr.Name, metaDir) {

				if _, err := vfs.MkdirAll(fs, doc, nil); err != nil {
					return err
				}
			}

		case tar.TypeReg:

			if path.Base(hdr.Name) == albumFile {
				err = createAlbum(fs, hdr, tr, dstDoc, db)
				if err != nil {
					return err
				}
			} else if path.Ext(hdr.Name) == contactExt {
				if err := createContact(fs, hdr, tr, db); err != nil {
					return err
				}
			} else {
				if err := createFile(fs, hdr, tr, dstDoc); err != nil {
					return err
				}
			}

		default:
			fmt.Println("Unknown typeflag", hdr.Typeflag)
			return errors.New("Unknown typeflag")

		}

	}

	return nil

}
