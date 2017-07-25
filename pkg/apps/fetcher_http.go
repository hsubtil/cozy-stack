package apps

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"github.com/cozy/cozy-stack/pkg/utils"
	"github.com/sirupsen/logrus"
)

var httpClient = http.Client{Timeout: 20 * time.Second}

type httpFetcher struct {
	manFilename string
	prefix      string
	log         *logrus.Entry
}

func newHTTPFetcher(manFilename string, log *logrus.Entry) *httpFetcher {
	return &httpFetcher{
		manFilename: manFilename,
		log:         log,
	}
}

func (f *httpFetcher) FetchManifest(src *url.URL) (r io.ReadCloser, err error) {
	req, err := http.NewRequest(http.MethodGet, src.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			resp.Body.Close()
		}
	}()
	if resp.StatusCode != 200 {
		return nil, ErrManifestNotReachable
	}

	var reader io.Reader = resp.Body

	contentType := resp.Header.Get("Content-Type")
	switch contentType {
	case
		"application/gzip",
		"application/x-gzip",
		"application/x-tgz",
		"application/tar+gzip":
		reader, err = gzip.NewReader(reader)
		if err != nil {
			return nil, ErrManifestNotReachable
		}
	}

	tarReader := tar.NewReader(reader)
	for {
		hdr, err := tarReader.Next()
		if err == io.EOF {
			return nil, ErrManifestNotReachable
		}
		if err != nil {
			return nil, ErrManifestNotReachable
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		baseName := path.Base(hdr.Name)
		if baseName != f.manFilename {
			continue
		}
		if baseName != hdr.Name {
			f.prefix = path.Dir(hdr.Name) + "/"
		}
		return utils.ReadCloser(tarReader, func() error {
			return resp.Body.Close()
		}), nil
	}
}

func (f *httpFetcher) Fetch(src *url.URL, fs Copier, man Manifest) (err error) {
	exists, err := fs.Start(man.Slug(), man.Version())
	if err != nil {
		return err
	}
	defer func() {
		if errc := fs.Close(); errc != nil && err == nil {
			err = errc
		}
	}()
	if exists {
		return nil
	}

	req, err := http.NewRequest(http.MethodGet, src.String(), nil)
	if err != nil {
		return err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return ErrNotFound
	}

	var reader io.Reader = resp.Body
	var shasum []byte
	var h hash.Hash

	if frag := src.Fragment; frag != "" {
		shasum, err = hex.DecodeString(frag)
		if err == nil {
			h = sha256.New()
			reader = io.TeeReader(reader, h)
		}
	}

	contentType := resp.Header.Get("Content-Type")
	switch contentType {
	case
		"application/gzip",
		"application/x-gzip",
		"application/x-tgz",
		"application/tar+gzip":
		reader, err = gzip.NewReader(reader)
	}
	if err != nil {
		return err
	}

	tarReader := tar.NewReader(reader)
	for {
		hdr, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		name := hdr.Name
		if len(f.prefix) > 0 && strings.HasPrefix(name, f.prefix) {
			name = name[len(f.prefix):]
		}
		err = fs.Copy(&fileInfo{
			name: name,
			size: hdr.Size,
			mode: os.FileMode(hdr.Mode),
		}, tarReader)
		if err != nil {
			return err
		}
	}
	if len(shasum) > 0 && !bytes.Equal(shasum, h.Sum(nil)) {
		return ErrBadChecksum
	}
	return nil
}
