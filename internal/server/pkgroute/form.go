package pkgroute

import (
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"slices"

	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/blob"
)

// maxFormFields bounds what ReadForm holds in memory of the fields beside
// the file part: 10 MiB, the allowance mime/multipart's ReadForm makes for
// non-file parts. The largest field a client sends is PyPI's description, a
// project's README.
const maxFormFields = 10 << 20

// fieldOverhead is what ReadForm charges each field beyond its name and
// value, as mime/multipart's ReadForm charges for its map entries, so that
// a form of many empty fields is bounded too.
const fieldOverhead = 100

// Form is a multipart/form-data upload that ReadForm read.
type Form struct {
	// File is the content of the file part, committed to the blob store
	// as Upload commits it; StoreNew stores it. Content no item comes to
	// reference is collected as an orphan.
	File blob.Info
	// FileName is the file part's file name as the client sent it, and
	// Field its form name.
	FileName, Field string
	// Fields are the values of the fields that are not files, by name, in
	// the order sent.
	Fields url.Values
	// ExtraFiles contains explicitly requested additional file fields. Most
	// protocols discard extra files; signed LuaRocks uploads keep their .asc
	// sidecar beside the rock or rockspec.
	ExtraFiles map[string]FormFile
}

// FormFile is one optional multipart file retained by ReadFormWith.
type FormFile struct {
	File     blob.Info
	FileName string
}

// ReadForm reads the request's multipart/form-data body as it arrives, for
// twine's upload and NuGet's push (decisions 0037 and 0038): the first part
// with a file name whose form name is file, or with any name when file is
// "", streams into the blob store under the request's repository's checksum
// policy, as Upload does, and the fields that are not files are kept in
// Fields, whichever side of the file part they come. Later file parts are
// read and dropped, as Microsoft specifies for a NuGet push.
//
// Its refusals, in the protocol's envelope through Error: 415 for a body
// that is not multipart/form-data, 400 for one that is malformed or breaks
// off, 400 when no part matches, and 413 for fields over 10 MiB. The file
// part has no limit of its own, as a deploy through the item routes has
// none. The caller checks that the caller may deploy before it reads the
// body.
func (k *Kit) ReadForm(r *http.Request, file string) (Form, error) {
	return k.ReadFormWith(r, file)
}

// ReadFormWith reads the primary file and explicitly named optional file
// fields. It leaves all other file parts discarded, as ReadForm always did.
func (k *Kit) ReadFormWith(r *http.Request, file string, extras ...string) (Form, error) {
	mt, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "multipart/form-data" || params["boundary"] == "" {
		return Form{}, apierror.New(http.StatusUnsupportedMediaType,
			"The request body must be multipart/form-data")
	}
	mr := multipart.NewReader(r.Body, params["boundary"])
	f := Form{Fields: url.Values{}, ExtraFiles: map[string]FormFile{}}
	found := false
	budget := int64(maxFormFields)
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return Form{}, malformedForm(err)
		}
		name, fileName := part.FormName(), part.FileName()
		switch {
		case fileName != "" && !found && (file == "" || name == file):
			info, err := k.uploadPart(r, part)
			if err != nil {
				return Form{}, err
			}
			f.File, f.FileName, f.Field, found = info, fileName, name, true
		case fileName != "" && slices.Contains(extras, name) && f.ExtraFiles[name].FileName == "":
			info, err := k.uploadPart(r, part)
			if err != nil {
				return Form{}, err
			}
			f.ExtraFiles[name] = FormFile{File: info, FileName: fileName}
		case fileName != "":
			if _, err := io.Copy(io.Discard, part); err != nil {
				return Form{}, malformedForm(err)
			}
		default:
			if budget -= int64(len(name)) + fieldOverhead; budget < 0 {
				return Form{}, fieldsTooLarge()
			}
			v, err := io.ReadAll(io.LimitReader(part, budget+1))
			if err != nil {
				return Form{}, malformedForm(err)
			}
			if budget -= int64(len(v)); budget < 0 {
				return Form{}, fieldsTooLarge()
			}
			f.Fields.Add(name, string(v))
		}
	}
	if !found {
		if file == "" {
			return Form{}, badRequestf("The request has no file part")
		}
		return Form{}, badRequestf("The request has no file part named '%s'", file)
	}
	return f, nil
}

// uploadPart commits part to the blob store. A body that breaks off is the
// client's failure, answered 400 and never logged as a fault, and only a
// failure of the store itself is the item service's error.
func (k *Kit) uploadPart(r *http.Request, part io.Reader) (blob.Info, error) {
	ctx := r.Context()
	body := &readErr{r: part}
	info, err := k.o.Items().Upload(ctx, item.Deploy{
		Target: item.Target{Repo: RequestFrom(r).Repo}, Body: body,
	})
	switch {
	case body.err != nil:
		return blob.Info{}, malformedForm(body.err)
	case err != nil:
		return blob.Info{}, k.o.ItemError(ctx, "deploy", err)
	}
	return info, nil
}

// readErr keeps the first error other than io.EOF that r returns.
type readErr struct {
	r   io.Reader
	err error
}

func (e *readErr) Read(p []byte) (int, error) {
	n, err := e.r.Read(p)
	if err != nil && !errors.Is(err, io.EOF) && e.err == nil {
		e.err = err
	}
	return n, err //nolint:wrapcheck // the reader's own error, which io.Copy compares with io.EOF
}

func malformedForm(err error) error {
	return badRequestf("malformed multipart/form-data body: %v", err)
}

func fieldsTooLarge() error {
	return apierror.New(http.StatusRequestEntityTooLarge,
		fmt.Sprintf("the form's fields exceed %d bytes", maxFormFields))
}
