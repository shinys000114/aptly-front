# aptly-front

Server-side rendered Go frontend for a single internal aptly API server.

## Configuration

```sh
export APTLY_API_URL=http://127.0.0.1:8080
export APTLY_FRONT_LISTEN=:8088
```

## Manual run

```sh
go run .
```

Then open:

```text
http://127.0.0.1:8088/
```

## Notes

- The app assumes an unauthenticated internal aptly API.
- Dedicated pages cover repos, mirrors, snapshots, published repositories, files, tasks, and the repo/mirror/snapshot/publish relation graph.
- List pages include client-side filtering and selected-item bulk actions for destructive operations.
- Repo detail pages include package listing via `/api/repos/{name}/packages`, snapshot creation, and direct local repo publish.
- The API console can call any aptly `/api/...` endpoint that is not yet represented by a dedicated form.
- The graph is synthesized from aptly API responses. It handles both snapshot-to-publish and local-repo-to-publish sources, but does not read aptly's local database directly like `aptly graph`.
