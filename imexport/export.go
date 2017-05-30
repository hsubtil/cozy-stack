package main

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"

	"github.com/cozy/cozy-stack/client"
	"github.com/cozy/cozy-stack/client/auth"
	"github.com/cozy/cozy-stack/client/request"
)

func export(tw *tar.Writer, opts *request.Options, authReq *auth.Request) error {
	cClient := &client.Client{
		Domain: authReq.Domain,
		Scheme: opts.Scheme,
		Client: opts.Client,

		AuthClient: authReq.ClientParams,
		AuthScopes: authReq.Scopes,
		Authorizer: opts.Authorizer,
		UserAgent:  opts.UserAgent,
	}

	root := "/Documents"

	err := cClient.WalkByPath(root, func(path string, doc *client.DirOrFile, err error) error {
		fmt.Printf("Visited: %s  type: %s\n", path, doc.Attrs.Type)

		if doc.Attrs.Type == client.DirType {
			fmt.Println("directory")
		} else if doc.Attrs.Type == client.FileType {
			readCloser, err := cClient.DownloadByPath(path)
			if err != nil {
				return err
			}

			hdr := &tar.Header{
				Name: path,
				//Mode:       int64(doc.Attrs.Tags), //convertir doc.Attrs.Tags en int de permissions
				Size:       doc.Attrs.Size,
				ModTime:    doc.Attrs.CreatedAt,
				AccessTime: doc.Attrs.CreatedAt,
				ChangeTime: doc.Attrs.UpdatedAt,
			}
			if err := tw.WriteHeader(hdr); err != nil {
				return err
			}
			if _, err := io.Copy(tw, readCloser); err != nil {
				return err
			}

		} else {
			fmt.Println("type not found")
		}
		return err
	})

	if err != nil {
		return err
	}

	return nil
}

func tardir(w io.Writer, opts *request.Options, authReq *auth.Request) error {
	fmt.Println("tarball")
	//gzip writer
	gw := gzip.NewWriter(w)
	defer gw.Close()

	//tar writer
	tw := tar.NewWriter(gw)
	defer tw.Close()

	err := export(tw, opts, authReq)
	if err != nil {
		return err
	}

	return nil
}
