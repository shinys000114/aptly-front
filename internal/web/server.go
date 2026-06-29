package web

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"aptly-front/internal/aptly"
)

//go:embed templates/*.tmpl static/*
var assets embed.FS

type Server struct {
	client    *aptly.Client
	templates *template.Template
}

type Page struct {
	Title   string
	Active  string
	APIURL  string
	Flash   string
	Error   string
	Content any
}

func NewServer(client *aptly.Client) (*Server, error) {
	funcs := template.FuncMap{
		"join":        strings.Join,
		"json":        prettyJSON,
		"rawjson":     rawJSON,
		"field":       field,
		"stringSlice": stringSlice,
		"boolText":    boolText,
		"pathEscape":  url.PathEscape,
		"firstComponent": func(sources []aptly.PublishedRepo) string {
			if len(sources) > 0 && sources[0].Component != "" {
				return sources[0].Component
			}
			return "main"
		},
		"publishTarget": func(storage, prefix, distribution string) string {
			values := url.Values{}
			values.Set("storage", storage)
			values.Set("prefix", prefix)
			values.Set("distribution", distribution)
			return values.Encode()
		},
		"publishDetailPath": publishDetailTarget,
	}
	tmpl, err := template.New("").Funcs(funcs).ParseFS(assets, "templates/*.tmpl")
	if err != nil {
		return nil, err
	}
	return &Server{client: client, templates: tmpl}, nil
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/static/", http.FileServer(http.FS(assets)))
	mux.HandleFunc("/", s.dashboard)
	mux.HandleFunc("/repos", s.repos)
	mux.HandleFunc("/repos/", s.repoDetail)
	mux.HandleFunc("/mirrors", s.mirrors)
	mux.HandleFunc("/mirrors/", s.mirrorDetail)
	mux.HandleFunc("/snapshots", s.snapshots)
	mux.HandleFunc("/snapshots/", s.snapshotDetail)
	mux.HandleFunc("/publish", s.publish)
	mux.HandleFunc("/publish/detail", s.publishDetail)
	mux.HandleFunc("/tasks", s.tasks)
	mux.HandleFunc("/files", s.files)
	mux.HandleFunc("/graph", s.graph)
	mux.HandleFunc("/graph.json", s.graphJSON)
	mux.HandleFunc("/api-console", s.apiConsole)
	return recoverer(mux)
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}

	ctx := r.Context()
	data := dashboardData{}
	data.Version, data.VersionErr = s.client.Version(ctx)
	data.Repos, data.ReposErr = s.client.ListRepos(ctx)
	data.Mirrors, data.MirrorsErr = s.client.ListMirrors(ctx)
	data.Snapshots, data.SnapshotsErr = s.client.ListSnapshots(ctx)
	data.Publishes, data.PublishesErr = s.client.ListPublishes(ctx)

	s.render(w, http.StatusOK, "dashboard", Page{
		Title:   "Dashboard",
		Active:  "dashboard",
		APIURL:  s.client.BaseURL(),
		Flash:   r.URL.Query().Get("flash"),
		Content: data,
	})
}

func (s *Server) repos(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		repos, err := s.client.ListRepos(r.Context())
		s.render(w, statusFromErr(err), "repos", Page{
			Title:   "Repos",
			Active:  "repos",
			APIURL:  s.client.BaseURL(),
			Error:   errText(err),
			Flash:   r.URL.Query().Get("flash"),
			Content: repos,
		})
	case http.MethodPost:
		if err := parseRequestForm(r); err != nil {
			s.redirectErr(w, r, "/repos", err)
			return
		}
		action := r.FormValue("action")
		var err error
		switch action {
		case "create":
			err = s.client.CreateRepo(r.Context(), aptly.Repo{
				Name:                r.FormValue("name"),
				Comment:             r.FormValue("comment"),
				DefaultDistribution: r.FormValue("distribution"),
				DefaultComponent:    r.FormValue("component"),
			})
		case "delete":
			err = eachValue(formValues(r, "name"), func(name string) error {
				return s.client.DeleteRepo(r.Context(), name, checked(r, "force"))
			})
		case "snapshot":
			repo := r.FormValue("repo")
			err = s.client.SnapshotRepo(r.Context(), repo, r.FormValue("snapshot"), r.FormValue("description"))
			s.redirectResult(w, r, repoTarget(repo), err)
			return
		case "add_package_refs":
			repo := r.FormValue("repo")
			refs := refsFromText(r.FormValue("package_refs"))
			if len(refs) == 0 {
				err = fmt.Errorf("no package refs provided")
			} else {
				err = s.client.AddRepoPackageRefs(r.Context(), repo, refs)
			}
			s.redirectResult(w, r, repoTarget(repo), err)
			return
		case "delete_package_refs":
			repo := r.FormValue("repo")
			refs := formValues(r, "package_ref")
			if len(refs) == 0 {
				err = fmt.Errorf("nothing selected")
			} else {
				err = s.client.DeleteRepoPackageRefs(r.Context(), repo, refs)
			}
			s.redirectResult(w, r, repoTarget(repo), err)
			return
		case "add_file_dir":
			repo := r.FormValue("repo")
			err = s.client.AddRepoFileDir(
				r.Context(),
				repo,
				r.FormValue("dir"),
				checked(r, "no_remove"),
				checked(r, "force_replace"),
			)
			s.redirectResult(w, r, repoTarget(repo), err)
			return
		case "upload_deb":
			repo := r.FormValue("repo")
			err = s.uploadDebs(r)
			s.redirectResult(w, r, repoTarget(repo), err)
			return
		default:
			err = fmt.Errorf("unknown repo action %q", action)
		}
		s.redirectResult(w, r, "/repos", err)
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) repoDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	name, err := pathValue(r.URL.Path, "/repos/")
	if err != nil || name == "" {
		http.NotFound(w, r)
		return
	}
	detail, err := s.client.GetRepo(r.Context(), name)
	queryText := r.URL.Query().Get("q")
	var packages []packageRef
	var packagesErr error
	var files []string
	var filesErr error
	if err == nil {
		refs, refsErr := s.client.ListRepoPackages(r.Context(), name, queryText)
		packages = parsePackageRefs(refs)
		packagesErr = refsErr
		files, filesErr = s.client.ListFiles(r.Context())
		sort.Strings(files)
	}
	s.render(w, statusFromErr(err), "detail", Page{
		Title:  "Repo " + name,
		Active: "repos",
		APIURL: s.client.BaseURL(),
		Error:  errText(err),
		Content: detailData{
			Kind:         "repo",
			Name:         name,
			Entity:       detail,
			Packages:     packages,
			PackageQuery: queryText,
			PackagesErr:  errText(packagesErr),
			FileDirs:     files,
			FileDirsErr:  errText(filesErr),
		},
	})
}

func (s *Server) mirrors(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		mirrors, err := s.client.ListMirrors(r.Context())
		s.render(w, statusFromErr(err), "mirrors", Page{
			Title:   "Mirrors",
			Active:  "mirrors",
			APIURL:  s.client.BaseURL(),
			Error:   errText(err),
			Flash:   r.URL.Query().Get("flash"),
			Content: mirrors,
		})
	case http.MethodPost:
		if err := parseRequestForm(r); err != nil {
			s.redirectErr(w, r, "/mirrors", err)
			return
		}
		action := r.FormValue("action")
		var err error
		switch action {
		case "create":
			err = s.client.CreateMirror(r.Context(), aptly.Mirror{
				Name:             r.FormValue("name"),
				ArchiveURL:       r.FormValue("url"),
				Distribution:     r.FormValue("distribution"),
				Components:       splitInput(r.FormValue("components")),
				Architectures:    splitInput(r.FormValue("architectures")),
				Filter:           r.FormValue("filter"),
				FilterWithDeps:   checked(r, "filter_with_deps"),
				IgnoreSignatures: checked(r, "ignore_signatures"),
			})
		case "update":
			names := formValues(r, "name")
			err = eachValue(names, func(name string) error {
				return s.client.UpdateMirror(r.Context(), name)
			})
			if len(names) == 1 {
				s.redirectResult(w, r, mirrorTarget(names[0]), err)
				return
			}
		case "delete":
			err = eachValue(formValues(r, "name"), func(name string) error {
				return s.client.DeleteMirror(r.Context(), name, checked(r, "force"))
			})
		case "snapshot":
			mirror := r.FormValue("mirror")
			err = s.client.SnapshotMirror(r.Context(), mirror, r.FormValue("snapshot"), r.FormValue("description"))
			s.redirectResult(w, r, mirrorTarget(mirror), err)
			return
		default:
			err = fmt.Errorf("unknown mirror action %q", action)
		}
		s.redirectResult(w, r, "/mirrors", err)
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) mirrorDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	name, err := pathValue(r.URL.Path, "/mirrors/")
	if err != nil || name == "" {
		http.NotFound(w, r)
		return
	}
	detail, err := s.client.GetMirror(r.Context(), name)
	s.render(w, statusFromErr(err), "detail", Page{
		Title:  "Mirror " + name,
		Active: "mirrors",
		APIURL: s.client.BaseURL(),
		Error:  errText(err),
		Content: detailData{
			Kind:   "mirror",
			Name:   name,
			Entity: detail,
		},
	})
}

func (s *Server) snapshots(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		snapshots, err := s.client.ListSnapshots(r.Context())
		s.render(w, statusFromErr(err), "snapshots", Page{
			Title:   "Snapshots",
			Active:  "snapshots",
			APIURL:  s.client.BaseURL(),
			Error:   errText(err),
			Flash:   r.URL.Query().Get("flash"),
			Content: snapshots,
		})
	case http.MethodPost:
		if err := parseRequestForm(r); err != nil {
			s.redirectErr(w, r, "/snapshots", err)
			return
		}
		action := r.FormValue("action")
		var err error
		switch action {
		case "delete":
			err = eachValue(formValues(r, "name"), func(name string) error {
				return s.client.DeleteSnapshot(r.Context(), name, checked(r, "force"))
			})
		default:
			err = fmt.Errorf("unknown snapshot action %q", action)
		}
		s.redirectResult(w, r, "/snapshots", err)
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) snapshotDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	name, err := pathValue(r.URL.Path, "/snapshots/")
	if err != nil || name == "" {
		http.NotFound(w, r)
		return
	}
	detail, err := s.client.GetSnapshot(r.Context(), name)
	s.render(w, statusFromErr(err), "detail", Page{
		Title:  "Snapshot " + name,
		Active: "snapshots",
		APIURL: s.client.BaseURL(),
		Error:  errText(err),
		Content: detailData{
			Kind:   "snapshot",
			Name:   name,
			Entity: detail,
		},
	})
}

func (s *Server) publish(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		data := publishData{}
		data.Publishes, data.PublishErr = s.client.ListPublishes(r.Context())
		err := data.PublishErr
		s.render(w, statusFromErr(err), "publish", Page{
			Title:   "Publish",
			Active:  "publish",
			APIURL:  s.client.BaseURL(),
			Error:   errText(err),
			Flash:   r.URL.Query().Get("flash"),
			Content: data,
		})
	case http.MethodPost:
		if err := parseRequestForm(r); err != nil {
			s.redirectErr(w, r, "/publish", err)
			return
		}
		action := r.FormValue("action")
		var err error
		switch action {
		case "create":
			prefix := prefixOrDot(r.FormValue("prefix"))
			distribution := r.FormValue("distribution")
			err = s.client.CreatePublish(
				r.Context(),
				prefix,
				distribution,
				sourceKind(r.FormValue("source_kind")),
				r.FormValue("component"),
				r.FormValue("source_name"),
				r.FormValue("architectures"),
				signingFromForm(r),
			)
			s.redirectResult(w, r, publishDetailTarget("", prefix, distribution), err)
			return
		case "switch":
			prefix := prefixOrDot(r.FormValue("prefix"))
			distribution := r.FormValue("distribution")
			storage := r.FormValue("storage")
			err = s.client.SwitchPublish(
				r.Context(),
				prefix,
				distribution,
				r.FormValue("component"),
				r.FormValue("snapshot"),
				signingFromForm(r),
			)
			s.redirectResult(w, r, publishDetailTarget(storage, prefix, distribution), err)
			return
		case "drop":
			targets := formValues(r, "target")
			if len(targets) == 0 {
				err = s.client.DropPublish(r.Context(), prefixOrDot(r.FormValue("prefix")), r.FormValue("distribution"), checked(r, "force"))
			} else {
				err = eachValue(targets, func(target string) error {
					prefix, distribution, parseErr := parsePublishTarget(target)
					if parseErr != nil {
						return parseErr
					}
					return s.client.DropPublish(r.Context(), prefixOrDot(prefix), distribution, checked(r, "force"))
				})
			}
		default:
			err = fmt.Errorf("unknown publish action %q", action)
		}
		s.redirectResult(w, r, "/publish", err)
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) publishDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}

	storage := r.URL.Query().Get("storage")
	prefix := r.URL.Query().Get("prefix")
	distribution := r.URL.Query().Get("distribution")
	publishes, publishErr := s.client.ListPublishes(r.Context())
	snapshots, snapshotsErr := s.client.ListSnapshots(r.Context())
	err := firstErr(publishErr, snapshotsErr)

	var publish aptly.Publish
	if err == nil {
		var ok bool
		publish, ok = findPublish(publishes, storage, prefix, distribution)
		if !ok {
			err = fmt.Errorf("published repository not found")
		}
	}

	s.render(w, statusFromErr(err), "publish_detail", Page{
		Title:  "Published " + distribution,
		Active: "publish",
		APIURL: s.client.BaseURL(),
		Error:  errText(err),
		Flash:  r.URL.Query().Get("flash"),
		Content: publishDetailData{
			Publish:   publish,
			Snapshots: snapshots,
		},
	})
}

func (s *Server) uploadDebs(r *http.Request) error {
	repo := strings.TrimSpace(r.FormValue("repo"))
	dir := strings.TrimSpace(r.FormValue("upload_dir"))
	if repo == "" {
		return fmt.Errorf("repo is required")
	}
	if dir == "" {
		dir = "aptly-front-" + repo
	}
	if r.MultipartForm == nil || len(r.MultipartForm.File["deb_files"]) == 0 {
		return fmt.Errorf("no files selected")
	}

	for _, header := range r.MultipartForm.File["deb_files"] {
		file, err := header.Open()
		if err != nil {
			return err
		}
		err = s.client.UploadFile(r.Context(), dir, header.Filename, file)
		file.Close()
		if err != nil {
			return err
		}
	}

	if checked(r, "add_after_upload") {
		return s.client.AddRepoFileDir(
			r.Context(),
			repo,
			dir,
			checked(r, "no_remove"),
			checked(r, "force_replace"),
		)
	}
	return nil
}

func (s *Server) tasks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	tasks, err := s.client.ListTasks(r.Context())
	s.render(w, statusFromErr(err), "tasks", Page{
		Title:   "Tasks",
		Active:  "tasks",
		APIURL:  s.client.BaseURL(),
		Error:   errText(err),
		Content: tasks,
	})
}

func (s *Server) files(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	files, err := s.client.ListFiles(r.Context())
	sort.Strings(files)
	s.render(w, statusFromErr(err), "files", Page{
		Title:   "Files",
		Active:  "files",
		APIURL:  s.client.BaseURL(),
		Error:   errText(err),
		Content: files,
	})
}

func (s *Server) graph(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	graph, err := s.client.BuildGraph(r.Context())
	s.render(w, statusFromErr(err), "graph", Page{
		Title:   "Graph",
		Active:  "graph",
		APIURL:  s.client.BaseURL(),
		Error:   errText(err),
		Content: graph,
	})
}

func (s *Server) graphJSON(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	graph, err := s.client.BuildGraph(r.Context())
	if err != nil {
		http.Error(w, err.Error(), statusFromErr(err))
		return
	}
	writeJSON(w, graph)
}

func (s *Server) apiConsole(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.render(w, http.StatusOK, "api_console", Page{
			Title:  "API Console",
			Active: "api",
			APIURL: s.client.BaseURL(),
			Content: rawConsoleData{
				Method: "GET",
				Path:   "/api/repos",
			},
		})
	case http.MethodPost:
		if err := r.ParseForm(); err != nil {
			s.renderRawError(w, r, err)
			return
		}
		method := strings.ToUpper(strings.TrimSpace(r.FormValue("method")))
		apiPath := strings.TrimSpace(r.FormValue("path"))
		body := []byte(strings.TrimSpace(r.FormValue("body")))

		if method == "" {
			method = http.MethodGet
		}
		if !strings.HasPrefix(apiPath, "/api/") {
			s.renderRawError(w, r, fmt.Errorf("path must start with /api/"))
			return
		}
		if len(body) > 0 && !json.Valid(body) {
			s.renderRawError(w, r, fmt.Errorf("body is not valid JSON"))
			return
		}

		resp, err := s.client.Raw(r.Context(), method, apiPath, nil, body)
		data := rawConsoleData{
			Method:      method,
			Path:        apiPath,
			Body:        string(body),
			Status:      resp.Status,
			ContentType: resp.ContentType,
			Response:    formatJSON(resp.Body),
		}
		page := Page{Title: "API Console", Active: "api", APIURL: s.client.BaseURL(), Content: data, Error: errText(err)}
		s.render(w, statusFromErr(err), "api_console", page)
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) renderRawError(w http.ResponseWriter, r *http.Request, err error) {
	s.render(w, http.StatusBadRequest, "api_console", Page{
		Title:  "API Console",
		Active: "api",
		APIURL: s.client.BaseURL(),
		Error:  err.Error(),
		Content: rawConsoleData{
			Method: r.FormValue("method"),
			Path:   r.FormValue("path"),
			Body:   r.FormValue("body"),
		},
	})
}

func (s *Server) render(w http.ResponseWriter, status int, name string, page Page) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := s.templates.ExecuteTemplate(w, name, page); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) redirectResult(w http.ResponseWriter, r *http.Request, target string, err error) {
	if err != nil {
		s.redirectErr(w, r, target, err)
		return
	}
	redirect(w, r, target, "done")
}

func (s *Server) redirectErr(w http.ResponseWriter, r *http.Request, target string, err error) {
	redirect(w, r, target, "error: "+err.Error())
}

func redirect(w http.ResponseWriter, r *http.Request, target, flash string) {
	u, _ := url.Parse(target)
	query := u.Query()
	query.Set("flash", flash)
	u.RawQuery = query.Encode()
	http.Redirect(w, r, u.String(), http.StatusSeeOther)
}

func repoTarget(name string) string {
	if name == "" {
		return "/repos"
	}
	return "/repos/" + url.PathEscape(name)
}

func mirrorTarget(name string) string {
	if name == "" {
		return "/mirrors"
	}
	return "/mirrors/" + url.PathEscape(name)
}

func publishDetailTarget(storage, prefix, distribution string) string {
	values := url.Values{}
	values.Set("storage", storage)
	values.Set("prefix", prefix)
	values.Set("distribution", distribution)
	return "/publish/detail?" + values.Encode()
}

type dashboardData struct {
	Version      aptly.Version
	VersionErr   error
	Repos        []aptly.Repo
	ReposErr     error
	Mirrors      []aptly.Mirror
	MirrorsErr   error
	Snapshots    []aptly.Snapshot
	SnapshotsErr error
	Publishes    []aptly.Publish
	PublishesErr error
}

type detailData struct {
	Kind         string
	Name         string
	Entity       aptly.Entity
	Packages     []packageRef
	PackageQuery string
	PackagesErr  string
	FileDirs     []string
	FileDirsErr  string
}

type packageRef struct {
	Type    string
	Arch    string
	Name    string
	Version string
	Key     string
	Raw     string
}

type publishData struct {
	Publishes  []aptly.Publish
	PublishErr error
}

type publishDetailData struct {
	Publish   aptly.Publish
	Snapshots []aptly.Snapshot
}

type rawConsoleData struct {
	Method      string
	Path        string
	Body        string
	Status      int
	ContentType string
	Response    string
}

func methodNotAllowed(w http.ResponseWriter) {
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}

func parseRequestForm(r *http.Request) error {
	if strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "multipart/form-data") {
		return r.ParseMultipartForm(128 << 20)
	}
	return r.ParseForm()
}

func statusFromErr(err error) int {
	if err == nil {
		return http.StatusOK
	}
	var apiErr *aptly.APIError
	if ok := errorAs(err, &apiErr); ok && apiErr.Status >= 400 && apiErr.Status <= 599 {
		return apiErr.Status
	}
	return http.StatusBadGateway
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func errorAs(err error, target any) bool {
	switch t := target.(type) {
	case **aptly.APIError:
		apiErr, ok := err.(*aptly.APIError)
		if ok {
			*t = apiErr
		}
		return ok
	default:
		return false
	}
}

func checked(r *http.Request, key string) bool {
	value := strings.ToLower(r.FormValue(key))
	return value == "1" || value == "true" || value == "on" || value == "yes"
}

func signingFromForm(r *http.Request) aptly.PublishSigning {
	return aptly.PublishSigning{
		Skip:           checked(r, "skip_signing"),
		Batch:          checked(r, "sign_batch"),
		GpgKey:         strings.TrimSpace(r.FormValue("gpg_key")),
		Keyring:        strings.TrimSpace(r.FormValue("keyring")),
		SecretKeyring:  strings.TrimSpace(r.FormValue("secret_keyring")),
		PassphraseFile: strings.TrimSpace(r.FormValue("passphrase_file")),
	}
}

func firstErr(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

func splitInput(value string) []string {
	fields := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\n' || r == '\t'
	})
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field != "" {
			out = append(out, field)
		}
	}
	return out
}

func parsePackageRefs(refs []string) []packageRef {
	packages := make([]packageRef, 0, len(refs))
	for _, ref := range refs {
		fields := strings.Fields(ref)
		row := packageRef{Raw: ref}
		if len(fields) > 0 {
			row.Type, row.Arch = splitPackageTypeArch(fields[0])
		}
		if len(fields) > 1 {
			row.Name = fields[1]
		}
		if len(fields) > 2 {
			row.Version = fields[2]
		}
		if len(fields) > 3 {
			row.Key = strings.Join(fields[3:], " ")
		}
		packages = append(packages, row)
	}
	return packages
}

func refsFromText(value string) []string {
	lines := strings.Split(value, "\n")
	refs := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			refs = append(refs, line)
		}
	}
	return refs
}

func splitPackageTypeArch(value string) (string, string) {
	if value == "" {
		return "", ""
	}
	switch value[0] {
	case 'P', 'S':
		return value[:1], value[1:]
	default:
		return "", value
	}
}

func formValues(r *http.Request, key string) []string {
	values := r.Form[key]
	if len(values) == 0 && r.FormValue(key) != "" {
		values = []string{r.FormValue(key)}
	}

	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func eachValue(values []string, fn func(string) error) error {
	if len(values) == 0 {
		return fmt.Errorf("nothing selected")
	}
	var failures []string
	for _, value := range values {
		if err := fn(value); err != nil {
			failures = append(failures, value+": "+err.Error())
		}
	}
	if len(failures) > 0 {
		return errors.New(strings.Join(failures, "; "))
	}
	return nil
}

func pathValue(requestPath, prefix string) (string, error) {
	value := strings.TrimPrefix(requestPath, prefix)
	if value == requestPath {
		return "", fmt.Errorf("path %q does not have prefix %q", requestPath, prefix)
	}
	return url.PathUnescape(value)
}

func sourceKind(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "local", "repo":
		return "local"
	case "mirror":
		return "mirror"
	default:
		return "snapshot"
	}
}

func findPublish(publishes []aptly.Publish, storage, prefix, distribution string) (aptly.Publish, bool) {
	for _, publish := range publishes {
		if publish.Storage == storage && publish.Prefix == prefix && publish.Distribution == distribution {
			return publish, true
		}
	}
	if storage == "" {
		for _, publish := range publishes {
			if publish.Prefix == prefix && publish.Distribution == distribution {
				return publish, true
			}
		}
	}
	return aptly.Publish{}, false
}

func parsePublishTarget(target string) (string, string, error) {
	values, err := url.ParseQuery(target)
	if err != nil {
		return "", "", err
	}
	prefix := values.Get("prefix")
	distribution := values.Get("distribution")
	if distribution == "" {
		return "", "", fmt.Errorf("publish target has no distribution")
	}
	return prefix, distribution, nil
}

func prefixOrDot(prefix string) string {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return "."
	}
	return prefix
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(value)
}

func prettyJSON(value any) string {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Sprint(value)
	}
	return string(data)
}

func rawJSON(value any) template.JS {
	data, err := json.Marshal(value)
	if err != nil {
		return template.JS("null")
	}
	return template.JS(data)
}

func formatJSON(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	var out bytes.Buffer
	if json.Indent(&out, data, "", "  ") == nil {
		return out.String()
	}
	return string(data)
}

func field(entity aptly.Entity, key string) string {
	if entity == nil {
		return ""
	}
	value, ok := entity[key]
	if !ok || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return typed
	case float64:
		if typed == float64(int64(typed)) {
			return strconv.FormatInt(int64(typed), 10)
		}
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case bool:
		return boolText(typed)
	default:
		data, err := json.Marshal(typed)
		if err != nil {
			return fmt.Sprint(typed)
		}
		return string(data)
	}
}

func stringSlice(values []string) string {
	return strings.Join(values, ", ")
}

func boolText(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

func recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				http.Error(w, fmt.Sprint(recovered), http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}
