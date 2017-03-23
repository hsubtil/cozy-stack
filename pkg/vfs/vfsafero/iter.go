package vfsafero

import (
	"github.com/cozy/cozy-stack/pkg/consts"
	"github.com/cozy/cozy-stack/pkg/couchdb"
	"github.com/cozy/cozy-stack/pkg/couchdb/mango"
	"github.com/cozy/cozy-stack/pkg/vfs"
)

const iterMaxFetchSize = 100

// iter is a struct allowing to iterate over the children of a
// directory. The iterator is not thread-safe.
type iter struct {
	db     couchdb.Database
	sel    mango.Filter
	opt    *vfs.IteratorOptions
	list   []*vfs.DirOrFileDoc
	offset int
	index  int
	done   bool
}

// newIterator return a new iterator.
func newIterator(db couchdb.Database, dir *vfs.DirDoc, opt *vfs.IteratorOptions) vfs.DirIterator {
	if opt == nil {
		opt = &vfs.IteratorOptions{ByFetch: iterMaxFetchSize}
	}
	if opt.ByFetch == 0 || opt.ByFetch > iterMaxFetchSize {
		opt.ByFetch = iterMaxFetchSize
	}
	sel := mango.Equal("dir_id", dir.DocID)
	if opt.AfterID != "" {
		// TODO: adapt this code when filtering and sorting are added to the
		// iterator
		sel = mango.And(sel, mango.Gt("_id", opt.AfterID))
	}
	return &iter{
		db:  db,
		sel: sel,
		opt: opt,
	}
}

// Next should be called to get the next directory or file children of the
// parent directory. If the error is vfs.ErrIteratorDone
func (i *iter) Next() (*vfs.DirDoc, *vfs.FileDoc, error) {
	if i.done {
		return nil, nil, vfs.ErrIteratorDone
	}
	if i.index >= len(i.list) {
		if err := i.fetch(); err != nil {
			return nil, nil, err
		}
	}
	d, f := i.list[i.index].Refine()
	i.index++
	return d, f, nil
}

// fetch should be called when the index is out of the list boundary.
func (i *iter) fetch() error {
	l := len(i.list)
	if l > 0 && l < i.opt.ByFetch {
		i.done = true
		return vfs.ErrIteratorDone
	}

	i.offset += l
	i.index = 0
	i.list = i.list[:0]

	req := &couchdb.FindRequest{
		UseIndex: "dir-children",
		Selector: i.sel,
		Limit:    i.opt.ByFetch,
		Skip:     i.offset,
	}
	err := couchdb.FindDocs(i.db, consts.Files, req, &i.list)
	if err != nil {
		return err
	}
	if len(i.list) == 0 {
		return vfs.ErrIteratorDone
	}
	return nil
}
