[Table of contents](README.md#table-of-contents)

# Move, export and import

## Export

A Cozy's user can ask at any time to export a snapshot of all its data and
metadata. This export takes place asynchronously and is separated in two parts:

- a metadata tarball containing the in a JSON format all the doctypes
- multi-part files tarballs containing the files (or a subpart of the files).

The export process is part of a worker described in the
[workers section](./workers.md#export) of the documentation.

Endpoints described in this documentation require a permission on the
`io.cozy.exports` doctype.

### POST /move/exports

This endpoint can be used to create a new export job.

Exports options fields are:

-   `parts_size` (optional) (int): the size in bytes of a tarball files part.
-   `max_age` (optional) (duration / nanosecs): the maximum age of the export
    data.
-   `with_doctypes` (optional) (string array): the list of exported doctypes

#### Request

```http
POST /move/exports HTTP/1.1
Host: source.cozy.tools
Authorization: Bearer ...
Content-Type: application/vnd.api+json
```

```json
{
    "data": {
        "attributes": {
            "parts_size": 10240,
            "with_doctypes": []
        }
    }
}
```

### GET /move/exports/:opaque-identifier

This endpoint can be used to fetch the metadata of an export.

Exports fields are:

-   `parts_size` (int): the size in bytes of a tarball files part.
-   `parts_cursors` (string array): the list of cursors to access to the
    different files parts.
-   `parts_length` (int): number of parts
-   `with_doctypes` (string array): the list of exported doctypes
    (if empty of null, all doctypes are exported)
-   `state` (string): the state of the export (`"exporting"` / `"done"` /
    `"error"`).
-   `created_at` (string / time): the date of creation of the export
-   `expires_at` (string / time): the date of expiration of the export
-   `total_size` (int): the total size of the exported documents from CouchDB
-   `creation_duration` (int): the amount of nanoseconds taken for the creation
    of the export
-   `error` (string): an error string if the export is in an `"error"` state

#### Request

```http
GET /move/exports/XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX HTTP/1.1
Host: source.cozy.tools
Authorization: Bearer ...
Content-Type: application/vnd.api+json
```

```json
{
    "data": {
        "type": "io.cozy.exports",
        "id": "86dbb546ca49f0ed1ce0a1ff0d1b15e3",
        "meta": {
            "rev": "2-XXX"
        },
        "attributes": {
            "parts_size": 10240,
            "parts_cursors": ["AAA", "BBB", "CCC"],
            "with_doctypes": [],
            "state": "done",
            "created_at": "2018-05-04T08:59:37.530693972+02:00",
            "expires_at": "2018-05-11T08:59:37.530693972+02:00",
            "total_size": 1123,
            "creation_duration": 62978511,
            "error": ""
        }
    }
}
```

### GET /move/exports/data/:opaque-identifier?cursor=XXX

This endpoint will download an archive containing the metadata and files of the
user, as part of a multi-part download. The cursor given should be one of the
defined in the export document `parts_cursors`.

Only the first part of the data contains the metadata.

## Import

### POST /move/imports/precheck

This endpoint can be use to check that an export can be imported from the given
URL, before doing the real import.

#### Request

```http
POST /move/imports/precheck HTTP/1.1
Host: destination.cozy.tools
Authorization: Bearer ...
Content-Type: application/vnd.api+json
```

```json
{
    "data": {
        "attributes": {
            "url": "https://settings.source.cozy.tools/#/exports/XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX"
        }
    }
}
```

#### Responses

- `204 No Content` if every thing is fine
- `404 Not Found` if no archive can be found at the given URL
- `422 Entity Too Large` if the quota is too small to import the files

### POST /move/imports

This endpoint can be used to really start an import.

#### Request

```http
POST /move/imports HTTP/1.1
Host: destination.cozy.tools
Authorization: Bearer ...
Content-Type: application/vnd.api+json
```

```json
{
    "data": {
        "attributes": {
            "url": "https://settings.source.cozy.tools/#/exports/XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX"
        }
    }
}
```

#### Responses

```http
HTTP/1.1 303 See Other
Location: https://destination.cozy.tools/move/importing
```

### GET /move/importing

This shows a page for the user to wait that the import finishes.
