// Package files is the HTTP frontend of the vfs package. It exposes
// an HTTP api to manipulate the filesystem and offer all the
// possibilities given by the vfs.
package files

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/cozy/cozy-stack/pkg/consts"
	"github.com/cozy/cozy-stack/pkg/instance"
	pkgperm "github.com/cozy/cozy-stack/pkg/permissions"
	"github.com/cozy/cozy-stack/pkg/vfs"
	"github.com/cozy/cozy-stack/web/jsonapi"
	"github.com/cozy/cozy-stack/web/middlewares"
	"github.com/cozy/cozy-stack/web/permissions"
	"github.com/labstack/echo"
)

// TagSeparator is the character separating tags
const TagSeparator = ","

// ErrDocTypeInvalid is used when the document type sent is not
// recognized
var ErrDocTypeInvalid = errors.New("Invalid document type")

// CreationHandler handle all POST requests on /files/:dir-id
// aiming at creating a new document in the FS. Given the Type
// parameter of the request, it will either upload a new file or
// create a new directory.
func CreationHandler(c echo.Context) error {
	instance := middlewares.GetInstance(c)
	var doc jsonapi.Object
	var err error
	switch c.QueryParam("Type") {
	case consts.FileType:
		doc, err = createFileHandler(c, instance)
	case consts.DirType:
		doc, err = createDirHandler(c, instance)
	default:
		err = ErrDocTypeInvalid
	}

	if err != nil {
		return wrapVfsError(err)
	}

	return jsonapi.Data(c, http.StatusCreated, doc, nil)
}

func createFileHandler(c echo.Context, vfsC vfs.Context) (f *file, err error) {
	tags := strings.Split(c.QueryParam("Tags"), TagSeparator)

	dirID := c.Param("dir-id")
	name := c.QueryParam("Name")
	var doc *vfs.FileDoc
	doc, err = fileDocFromReq(c, name, dirID, tags)
	if err != nil {
		return
	}

	err = checkPerm(c, "POST", nil, doc)
	if err != nil {
		return
	}

	file, err := vfs.CreateFile(vfsC, doc, nil)
	if err != nil {
		return
	}

	defer func() {
		if cerr := file.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()

	_, err = io.Copy(file, c.Request().Body)
	f = newFile(doc)
	return
}

func createDirHandler(c echo.Context, vfsC vfs.Context) (*dir, error) {
	path := c.QueryParam("Path")
	tags := strings.Split(c.QueryParam("Tags"), TagSeparator)

	var doc *vfs.DirDoc
	var err error
	if path != "" {
		if c.QueryParam("Recursive") == "true" {
			doc, err = vfs.MkdirAll(vfsC, path, tags)
		} else {
			doc, err = vfs.Mkdir(vfsC, path, tags)
		}
		if err != nil {
			return nil, err
		}
		return newDir(doc), nil
	}

	dirID := c.Param("dir-id")
	name := c.QueryParam("Name")
	doc, err = vfs.NewDirDoc(name, dirID, tags)
	if err != nil {
		return nil, err
	}
	if date := c.Request().Header.Get("Date"); date != "" {
		if t, err2 := time.Parse(time.RFC1123, date); err2 == nil {
			doc.CreatedAt = t
			doc.UpdatedAt = t
		}
	}

	err = checkPerm(c, "POST", doc, nil)
	if err != nil {
		return nil, err
	}

	if err = vfs.CreateDir(vfsC, doc); err != nil {
		return nil, err
	}

	return newDir(doc), nil
}

// OverwriteFileContentHandler handles PUT requests on /files/:file-id
// to overwrite the content of a file given its identifier.
func OverwriteFileContentHandler(c echo.Context) (err error) {
	var instance = middlewares.GetInstance(c)
	var olddoc *vfs.FileDoc
	var newdoc *vfs.FileDoc

	olddoc, err = vfs.GetFileDoc(instance, c.Param("file-id"))
	if err != nil {
		return wrapVfsError(err)
	}

	newdoc, err = fileDocFromReq(
		c,
		olddoc.Name,
		olddoc.DirID,
		olddoc.Tags,
	)
	if err != nil {
		return wrapVfsError(err)
	}

	newdoc.ReferencedBy = olddoc.ReferencedBy

	if err = checkIfMatch(c, olddoc.Rev()); err != nil {
		return wrapVfsError(err)
	}

	err = checkPerm(c, permissions.PUT, nil, olddoc)
	if err != nil {
		return
	}

	err = checkPerm(c, permissions.PUT, nil, newdoc)
	if err != nil {
		return
	}

	file, err := vfs.CreateFile(instance, newdoc, olddoc)
	if err != nil {
		return wrapVfsError(err)
	}

	defer func() {
		if cerr := file.Close(); cerr != nil && err == nil {
			err = cerr
		}
		if err != nil {
			err = wrapVfsError(err)
			return
		}
		err = fileData(c, http.StatusOK, newdoc, nil)
	}()

	_, err = io.Copy(file, c.Request().Body)
	return
}

// ModifyMetadataByIDHandler handles PATCH requests on /files/:file-id
//
// It can be used to modify the file or directory metadata, as well as
// moving and renaming it in the filesystem.
func ModifyMetadataByIDHandler(c echo.Context) error {
	patch, err := getPatch(c)
	if err != nil {
		return wrapVfsError(err)
	}

	instance := middlewares.GetInstance(c)
	dir, file, err := vfs.GetDirOrFileDoc(instance, c.Param("file-id"))
	if err != nil {
		return wrapVfsError(err)
	}

	return applyPatch(c, instance, patch, dir, file)
}

// ModifyMetadataByPathHandler handles PATCH requests on /files/:file-id
//
// It can be used to modify the file or directory metadata, as well as
// moving and renaming it in the filesystem.
func ModifyMetadataByPathHandler(c echo.Context) error {
	patch, err := getPatch(c)
	if err != nil {
		return wrapVfsError(err)
	}

	instance := middlewares.GetInstance(c)
	dir, file, err := vfs.GetDirOrFileDocFromPath(instance, c.QueryParam("Path"))
	if err != nil {
		return wrapVfsError(err)
	}

	return applyPatch(c, instance, patch, dir, file)
}

func getPatch(c echo.Context) (*vfs.DocPatch, error) {
	var patch vfs.DocPatch

	obj, err := jsonapi.Bind(c.Request(), &patch)
	if err != nil {
		return nil, jsonapi.BadJSON()
	}

	if rel, ok := obj.GetRelationship("parent"); ok {
		rid, ok := rel.ResourceIdentifier()
		if !ok {
			return nil, jsonapi.BadJSON()
		}
		patch.DirID = &rid.ID
	}

	return &patch, nil
}

func applyPatch(c echo.Context, instance *instance.Instance, patch *vfs.DocPatch, dir *vfs.DirDoc, file *vfs.FileDoc) error {
	var rev string
	if dir != nil {
		rev = dir.Rev()
	} else {
		rev = file.Rev()
	}

	if err := checkIfMatch(c, rev); err != nil {
		return wrapVfsError(err)
	}

	if err := checkPerm(c, permissions.PATCH, dir, file); err != nil {
		return err
	}

	if file != nil {
		doc, err := vfs.ModifyFileMetadata(instance, file, patch)
		if err != nil {
			return wrapVfsError(err)
		}
		return fileData(c, http.StatusOK, doc, nil)
	}
	doc, err := vfs.ModifyDirMetadata(instance, dir, patch)
	if err != nil {
		return wrapVfsError(err)
	}
	return dirData(c, http.StatusOK, doc)
}

// ReadMetadataFromIDHandler handles all GET requests on /files/:file-
// id aiming at getting file metadata from its path.
func ReadMetadataFromIDHandler(c echo.Context) error {
	instance := middlewares.GetInstance(c)

	fileID := c.Param("file-id")

	dir, file, err := vfs.GetDirOrFileDoc(instance, fileID)
	if err != nil {
		return wrapVfsError(err)
	}

	if err := checkPerm(c, permissions.GET, dir, file); err != nil {
		return err
	}

	if dir != nil {
		return dirData(c, http.StatusOK, dir)
	}
	return fileData(c, http.StatusOK, file, nil)
}

// ReadMetadataFromPathHandler handles all GET requests on
// /files/metadata aiming at getting file metadata from its path.
func ReadMetadataFromPathHandler(c echo.Context) error {
	var err error

	instance := middlewares.GetInstance(c)

	dir, file, err := vfs.GetDirOrFileDocFromPath(instance, c.QueryParam("Path"))
	if err != nil {
		return wrapVfsError(err)
	}

	if err := checkPerm(c, permissions.GET, dir, file); err != nil {
		return err
	}

	if dir != nil {
		return dirData(c, http.StatusOK, dir)
	}
	return fileData(c, http.StatusOK, file, nil)
}

// ReadFileContentFromIDHandler handles all GET requests on /files/:file-id
// aiming at downloading a file given its ID. It serves the file in inline
// mode.
func ReadFileContentFromIDHandler(c echo.Context) error {
	instance := middlewares.GetInstance(c)

	doc, err := vfs.GetFileDoc(instance, c.Param("file-id"))
	if err != nil {
		return wrapVfsError(err)
	}

	err = checkPerm(c, permissions.GET, nil, doc)
	if err != nil {
		return err
	}

	disposition := "inline"
	if c.QueryParam("Dl") == "1" {
		disposition = "attachment"
	}
	err = vfs.ServeFileContent(instance, doc, disposition, c.Request(), c.Response())
	if err != nil {
		return wrapVfsError(err)
	}

	return nil
}

func sendFileFromPath(c echo.Context, path string, checkPermission bool) error {
	instance := middlewares.GetInstance(c)

	doc, err := vfs.GetFileDocFromPath(instance, path)
	if err != nil {
		return wrapVfsError(err)
	}

	if checkPermission {
		err = permissions.Allow(c, "GET", doc)
		if err != nil {
			return err
		}
	}

	disposition := "inline"
	if c.QueryParam("Dl") == "1" {
		disposition = "attachment"
	}
	err = vfs.ServeFileContent(instance, doc, disposition, c.Request(), c.Response())
	if err != nil {
		return wrapVfsError(err)
	}

	return nil
}

// ReadFileContentFromPathHandler handles all GET request on /files/download
// aiming at downloading a file given its path. It serves the file in in
// attachment mode.
func ReadFileContentFromPathHandler(c echo.Context) error {
	return sendFileFromPath(c, c.QueryParam("Path"), true)
}

// ArchiveDownloadCreateHandler handles requests to /files/archive and stores the
// paremeters with a secret to be used in download handler below.s
func ArchiveDownloadCreateHandler(c echo.Context) error {
	archive := &vfs.Archive{}
	if _, err := jsonapi.Bind(c.Request(), archive); err != nil {
		return err
	}
	if len(archive.Files) == 0 {
		return c.JSON(http.StatusBadRequest, "Can't create an archive with no files")
	}
	if strings.Contains(archive.Name, "/") {
		return c.JSON(http.StatusBadRequest, "The archive filename can't contain a /")
	}
	if archive.Name == "" {
		archive.Name = "archive"
	}
	instance := middlewares.GetInstance(c)

	entries, err := archive.GetEntries(instance)
	if err != nil {
		return wrapVfsError(err)
	}

	for _, e := range entries {
		err = checkPerm(c, permissions.GET, e.Dir, e.File)
		if err != nil {
			return err
		}
	}

	// if accept header is application/zip, send the archive immediately
	if c.Request().Header.Get("Accept") == "application/zip" {
		return archive.Serve(instance, c.Response())
	}

	secret, err := vfs.GetStore(instance.Domain).AddArchive(archive)
	if err != nil {
		return wrapVfsError(err)
	}
	archive.Secret = secret

	fakeName := url.QueryEscape(archive.Name)

	links := &jsonapi.LinksList{
		Related: "/files/archive/" + secret + "/" + fakeName + ".zip",
	}

	return jsonapi.Data(c, http.StatusOK, archive, links)
}

// FileDownloadCreateHandler stores the required path into a secret
// usable for download handler below.
func FileDownloadCreateHandler(c echo.Context) error {
	instance := middlewares.GetInstance(c)
	var doc *vfs.FileDoc
	var err error
	var path string

	if path = c.QueryParam("Path"); path != "" {
		if doc, err = vfs.GetFileDocFromPath(instance, path); err != nil {
			return wrapVfsError(err)
		}
	} else if id := c.QueryParam("Id"); id != "" {
		if doc, err = vfs.GetFileDoc(instance, id); err != nil {
			return wrapVfsError(err)
		}
		if path, err = doc.Path(instance); err != nil {
			return wrapVfsError(err)
		}
	}

	err = permissions.Allow(c, "GET", doc)
	if err != nil {
		return err
	}

	secret, err := vfs.GetStore(instance.Domain).AddFile(path)
	if err != nil {
		return wrapVfsError(err)
	}

	links := &jsonapi.LinksList{
		Related: "/files/downloads/" + secret + "/" + doc.Name,
	}

	return fileData(c, http.StatusOK, doc, links)
}

// ArchiveDownloadHandler handles requests to /files/archive/:secret/whatever.zip
// and creates on the fly zip archive from the parameters linked to secret.
func ArchiveDownloadHandler(c echo.Context) error {
	instance := middlewares.GetInstance(c)
	secret := c.Param("secret")
	archive, err := vfs.GetStore(instance.Domain).GetArchive(secret)
	if err != nil {
		return wrapVfsError(err)
	}
	if archive == nil {
		return jsonapi.NewError(400, "Wrong download token")
	}
	return archive.Serve(instance, c.Response())
}

// FileDownloadHandler send a file that have previously be defined
// through FileDownloadCreateHandler
func FileDownloadHandler(c echo.Context) error {
	instance := middlewares.GetInstance(c)
	secret := c.Param("secret")
	path, err := vfs.GetStore(instance.Domain).GetFile(secret)
	if err != nil {
		return wrapVfsError(err)
	}
	if path == "" {
		return jsonapi.NewError(400, "Wrong download token")
	}
	return sendFileFromPath(c, path, false)
}

// TrashHandler handles all DELETE requests on /files/:file-id and
// moves the file or directory with the specified file-id to the
// trash.
func TrashHandler(c echo.Context) error {
	instance := middlewares.GetInstance(c)

	fileID := c.Param("file-id")

	dir, file, err := vfs.GetDirOrFileDoc(instance, fileID)
	if err != nil {
		return wrapVfsError(err)
	}

	err = checkPerm(c, permissions.PUT, dir, file)
	if err != nil {
		return err
	}

	if dir != nil {
		doc, errt := vfs.TrashDir(instance, dir)
		if errt != nil {
			return wrapVfsError(errt)
		}
		return dirData(c, http.StatusOK, doc)
	}

	doc, errt := vfs.TrashFile(instance, file)
	if errt != nil {
		return wrapVfsError(errt)
	}
	return fileData(c, http.StatusOK, doc, nil)
}

// ReadTrashFilesHandler handle GET requests on /files/trash and return the
// list of trashed files and directories
func ReadTrashFilesHandler(c echo.Context) error {
	instance := middlewares.GetInstance(c)

	trash, err := vfs.GetDirDoc(instance, consts.TrashDirID)
	if err != nil {
		return wrapVfsError(err)
	}

	err = checkPerm(c, permissions.GET, trash, nil)
	if err != nil {
		return err
	}

	return dirDataList(c, http.StatusOK, trash)
}

// RestoreTrashFileHandler handle POST requests on /files/trash/file-id and
// can be used to restore a file or directory from the trash.
func RestoreTrashFileHandler(c echo.Context) error {
	instance := middlewares.GetInstance(c)

	fileID := c.Param("file-id")

	dir, file, err := vfs.GetDirOrFileDoc(instance, fileID)
	if err != nil {
		return wrapVfsError(err)
	}

	err = checkPerm(c, permissions.PUT, dir, file)
	if err != nil {
		return err
	}

	if dir != nil {
		doc, errt := vfs.RestoreDir(instance, dir)
		if errt != nil {
			return wrapVfsError(errt)
		}
		return dirData(c, http.StatusOK, doc)
	}

	doc, errt := vfs.RestoreFile(instance, file)
	if errt != nil {
		return wrapVfsError(errt)
	}
	return fileData(c, http.StatusOK, doc, nil)
}

// ClearTrashHandler handles DELETE request to clear the trash
func ClearTrashHandler(c echo.Context) error {
	instance := middlewares.GetInstance(c)

	trash, err := vfs.GetDirDoc(instance, consts.TrashDirID)
	if err != nil {
		return wrapVfsError(err)
	}

	err = checkPerm(c, permissions.DELETE, trash, nil)
	if err != nil {
		return err
	}

	err = vfs.DestroyDirContent(instance, trash)
	if err != nil {
		return wrapVfsError(err)
	}

	return c.NoContent(204)
}

// DestroyFileHandler handles DELETE request to clear one element from the trash
func DestroyFileHandler(c echo.Context) error {
	instance := middlewares.GetInstance(c)

	fileID := c.Param("file-id")

	dir, file, err := vfs.GetDirOrFileDoc(instance, fileID)
	if err != nil {
		return wrapVfsError(err)
	}

	err = checkPerm(c, permissions.DELETE, dir, file)
	if err != nil {
		return err
	}

	if dir != nil {
		err = vfs.DestroyDirAndContent(instance, dir)
	} else {
		err = vfs.DestroyFile(instance, file)
	}
	if err != nil {
		return wrapVfsError(err)
	}

	return c.NoContent(204)
}

// Routes sets the routing for the files service
func Routes(router *echo.Group) {
	router.HEAD("/download", ReadFileContentFromPathHandler)
	router.GET("/download", ReadFileContentFromPathHandler)
	router.HEAD("/download/:file-id", ReadFileContentFromIDHandler)
	router.GET("/download/:file-id", ReadFileContentFromIDHandler)

	router.GET("/metadata", ReadMetadataFromPathHandler)
	router.GET("/:file-id", ReadMetadataFromIDHandler)

	router.PATCH("/metadata", ModifyMetadataByPathHandler)
	router.PATCH("/:file-id", ModifyMetadataByIDHandler)

	router.POST("/", CreationHandler)
	router.POST("/:dir-id", CreationHandler)
	router.PUT("/:file-id", OverwriteFileContentHandler)

	router.POST("/archive", ArchiveDownloadCreateHandler)
	router.GET("/archive/:secret/:fake-name", ArchiveDownloadHandler)

	router.POST("/downloads", FileDownloadCreateHandler)
	router.GET("/downloads/:secret/:fake-name", FileDownloadHandler)

	router.POST("/:file-id/relationships/referenced_by", AddReferencedHandler)
	router.DELETE("/:file-id/relationships/referenced_by", RemoveReferencedHandler)

	router.GET("/trash", ReadTrashFilesHandler)
	router.DELETE("/trash", ClearTrashHandler)

	router.POST("/trash/:file-id", RestoreTrashFileHandler)
	router.DELETE("/trash/:file-id", DestroyFileHandler)

	router.DELETE("/:file-id", TrashHandler)
}

// wrapVfsError returns a formatted error from a golang error emitted by the vfs
func wrapVfsError(err error) error {
	switch err {
	case ErrDocTypeInvalid:
		return jsonapi.InvalidAttribute("type", err)
	case vfs.ErrParentDoesNotExist:
		return jsonapi.NotFound(err)
	case vfs.ErrForbiddenDocMove:
		return jsonapi.PreconditionFailed("dir-id", err)
	case vfs.ErrIllegalFilename:
		return jsonapi.InvalidParameter("name", err)
	case vfs.ErrIllegalTime:
		return jsonapi.InvalidParameter("UpdatedAt", err)
	case vfs.ErrInvalidHash:
		return jsonapi.PreconditionFailed("Content-MD5", err)
	case vfs.ErrContentLengthMismatch:
		return jsonapi.PreconditionFailed("Content-Length", err)
	case vfs.ErrConflict:
		return jsonapi.Conflict(err)
	case vfs.ErrFileInTrash:
		return jsonapi.BadRequest(err)
	case vfs.ErrNonAbsolutePath:
		return jsonapi.BadRequest(err)
	case vfs.ErrDirNotEmpty:
		return jsonapi.BadRequest(err)
	}
	return err
}

func fileDocFromReq(c echo.Context, name, dirID string, tags []string) (*vfs.FileDoc, error) {
	header := c.Request().Header

	size, err := parseContentLength(header.Get("Content-Length"))
	if err != nil {
		err = jsonapi.InvalidParameter("Content-Length", err)
		return nil, err
	}

	var md5Sum []byte
	if md5Str := header.Get("Content-MD5"); md5Str != "" {
		md5Sum, err = parseMD5Hash(md5Str)
	}
	if err != nil {
		err = jsonapi.InvalidParameter("Content-MD5", err)
		return nil, err
	}

	cdate := time.Now()
	if date := header.Get("Date"); date != "" {
		if t, err := time.Parse(time.RFC1123, date); err == nil {
			cdate = t
		}
	}

	var mime, class string
	contentType := header.Get("Content-Type")
	if contentType == "" {
		mime, class = vfs.ExtractMimeAndClassFromFilename(name)
	} else {
		mime, class = vfs.ExtractMimeAndClass(contentType)
	}

	executable := c.QueryParam("Executable") == "true"
	return vfs.NewFileDoc(
		name,
		dirID,
		size,
		md5Sum,
		mime,
		class,
		cdate,
		executable,
		tags,
	)
}

func checkIfMatch(c echo.Context, rev string) error {
	ifMatch := c.Request().Header.Get("If-Match")
	revQuery := c.QueryParam("rev")
	var wantedRev string
	if ifMatch != "" {
		wantedRev = ifMatch
	}
	if revQuery != "" && wantedRev == "" {
		wantedRev = revQuery
	}
	if wantedRev != "" && rev != wantedRev {
		return jsonapi.PreconditionFailed("If-Match", fmt.Errorf("Revision does not match"))
	}
	return nil
}

func checkPerm(c echo.Context, v pkgperm.Verb, d *vfs.DirDoc, f *vfs.FileDoc) error {
	if d != nil {
		return permissions.AllowVFS(c, v, d)
	}

	return permissions.AllowVFS(c, v, f)
}

func parseMD5Hash(md5B64 string) ([]byte, error) {
	// Encoded md5 hash in base64 should at least have 22 caracters in
	// base64: 16*3/4 = 21+1/3
	//
	// The padding may add up to 2 characters (non useful). If we are
	// out of these boundaries we know we don't have a good hash and we
	// can bail immediately.
	if len(md5B64) < 22 || len(md5B64) > 24 {
		return nil, fmt.Errorf("Given Content-MD5 is invalid")
	}

	md5Sum, err := base64.StdEncoding.DecodeString(md5B64)
	if err != nil || len(md5Sum) != 16 {
		return nil, fmt.Errorf("Given Content-MD5 is invalid")
	}

	return md5Sum, nil
}

func parseContentLength(contentLength string) (int64, error) {
	if contentLength == "" {
		return -1, nil
	}

	size, err := strconv.ParseInt(contentLength, 10, 64)
	if err != nil {
		err = fmt.Errorf("Invalid content length")
	}
	return size, err
}
