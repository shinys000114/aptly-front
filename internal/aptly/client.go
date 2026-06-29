package aptly

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Client struct {
	baseURL *url.URL
	http    *http.Client
}

type APIError struct {
	Method string
	Path   string
	Status int
	Body   string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("%s %s: aptly API returned HTTP %d: %s", e.Method, e.Path, e.Status, e.Body)
}

func NewClient(base string, timeout time.Duration) (*Client, error) {
	if base == "" {
		return nil, fmt.Errorf("empty aptly API URL")
	}

	parsed, err := url.Parse(base)
	if err != nil {
		return nil, err
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("aptly API URL must include scheme and host")
	}

	return &Client{
		baseURL: parsed,
		http: &http.Client{
			Timeout: timeout,
		},
	}, nil
}

func (c *Client) BaseURL() string {
	return c.baseURL.String()
}

func (c *Client) doJSON(ctx context.Context, method, apiPath string, query url.Values, body any, out any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}

	req, err := c.newRequest(ctx, method, apiPath, query, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return apiError(method, apiPath, resp)
	}

	if out == nil {
		io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) Raw(ctx context.Context, method, apiPath string, query url.Values, body []byte) (RawResponse, error) {
	req, err := c.newRequest(ctx, method, apiPath, query, bytes.NewReader(body))
	if err != nil {
		return RawResponse{}, err
	}
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return RawResponse{}, err
	}
	defer resp.Body.Close()

	data, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if readErr != nil {
		return RawResponse{}, readErr
	}

	return RawResponse{
		Status:      resp.StatusCode,
		ContentType: resp.Header.Get("Content-Type"),
		Body:        data,
	}, nil
}

func (c *Client) UploadFile(ctx context.Context, dir, filename string, file io.Reader) error {
	reader, writerPipe := io.Pipe()
	writer := multipart.NewWriter(writerPipe)

	go func() {
		part, err := writer.CreateFormFile("file", filename)
		if err != nil {
			writerPipe.CloseWithError(err)
			return
		}
		if _, err := io.Copy(part, file); err != nil {
			writerPipe.CloseWithError(err)
			return
		}
		if err := writer.Close(); err != nil {
			writerPipe.CloseWithError(err)
			return
		}
		writerPipe.Close()
	}()

	req, err := c.newRequest(ctx, http.MethodPost, api("files", dir), nil, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return apiError(http.MethodPost, api("files", dir), resp)
	}
	io.Copy(io.Discard, resp.Body)
	return nil
}

func (c *Client) newRequest(ctx context.Context, method, apiPath string, query url.Values, body io.Reader) (*http.Request, error) {
	if !strings.HasPrefix(apiPath, "/") {
		apiPath = "/" + apiPath
	}
	if strings.Contains(apiPath, "?") {
		parsed, err := url.Parse(apiPath)
		if err != nil {
			return nil, err
		}
		apiPath = parsed.Path
		if query == nil {
			query = url.Values{}
		}
		for key, values := range parsed.Query() {
			for _, value := range values {
				query.Add(key, value)
			}
		}
	}

	u := *c.baseURL
	u.Path = strings.TrimRight(c.baseURL.Path, "/") + apiPath
	u.RawQuery = query.Encode()

	return http.NewRequestWithContext(ctx, method, u.String(), body)
}

func apiError(method, apiPath string, resp *http.Response) error {
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	body := strings.TrimSpace(string(data))
	if body == "" {
		body = http.StatusText(resp.StatusCode)
	}
	return &APIError{Method: method, Path: apiPath, Status: resp.StatusCode, Body: body}
}

func api(parts ...string) string {
	items := []string{"api"}
	items = append(items, parts...)
	return "/" + strings.Join(items, "/")
}

func splitCSV(value string) []string {
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

type RawResponse struct {
	Status      int
	ContentType string
	Body        []byte
}

type Entity map[string]any

type Repo struct {
	Name                string `json:"Name"`
	Comment             string `json:"Comment"`
	DefaultDistribution string `json:"DefaultDistribution"`
	DefaultComponent    string `json:"DefaultComponent"`
}

type Mirror struct {
	Name             string   `json:"Name"`
	ArchiveURL       string   `json:"ArchiveURL"`
	Distribution     string   `json:"Distribution"`
	Components       []string `json:"Components"`
	Architectures    []string `json:"Architectures"`
	Filter           string   `json:"Filter"`
	FilterWithDeps   bool     `json:"FilterWithDeps"`
	IgnoreSignatures bool     `json:"IgnoreSignatures"`
}

type Snapshot struct {
	Name        string   `json:"Name"`
	CreatedAt   string   `json:"CreatedAt"`
	Description string   `json:"Description"`
	SourceKind  string   `json:"SourceKind"`
	SourceIDs   []string `json:"SourceIDs"`
}

type PublishedRepo struct {
	Name      string `json:"Name"`
	Component string `json:"Component"`
}

type Publish struct {
	Storage       string          `json:"Storage"`
	Prefix        string          `json:"Prefix"`
	Distribution  string          `json:"Distribution"`
	SourceKind    string          `json:"SourceKind"`
	Architectures []string        `json:"Architectures"`
	Sources       []PublishedRepo `json:"Sources"`
}

type Task struct {
	ID          int    `json:"ID"`
	Type        string `json:"Type"`
	State       string `json:"State"`
	Description string `json:"Description"`
	CreatedAt   string `json:"CreatedAt"`
	FinishedAt  string `json:"FinishedAt"`
	Error       string `json:"Error"`
}

type Version struct {
	Version string `json:"Version"`
}

type PublishSigning struct {
	Skip           bool
	Batch          bool
	GpgKey         string
	Keyring        string
	SecretKeyring  string
	PassphraseFile string
}

func (c *Client) Version(ctx context.Context) (Version, error) {
	var raw json.RawMessage
	if err := c.doJSON(ctx, http.MethodGet, api("version"), nil, nil, &raw); err != nil {
		return Version{}, err
	}

	var out Version
	if err := json.Unmarshal(raw, &out); err == nil && out.Version != "" {
		return out, nil
	}

	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return Version{Version: text}, nil
	}

	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err == nil {
		for _, key := range []string{"version", "Version"} {
			if value, ok := fields[key].(string); ok {
				return Version{Version: value}, nil
			}
		}
	}

	return Version{Version: strings.TrimSpace(string(raw))}, nil
}

func (c *Client) ListRepos(ctx context.Context) ([]Repo, error) {
	var out []Repo
	return out, c.doJSON(ctx, http.MethodGet, api("repos"), nil, nil, &out)
}

func (c *Client) GetRepo(ctx context.Context, name string) (Entity, error) {
	var out Entity
	return out, c.doJSON(ctx, http.MethodGet, api("repos", name), nil, nil, &out)
}

func (c *Client) ListRepoPackages(ctx context.Context, name, queryText string) ([]string, error) {
	query := url.Values{}
	if queryText != "" {
		query.Set("q", queryText)
	}
	var out []string
	return out, c.doJSON(ctx, http.MethodGet, api("repos", name, "packages"), query, nil, &out)
}

func (c *Client) AddRepoPackageRefs(ctx context.Context, name string, refs []string) error {
	body := Entity{"PackageRefs": refs}
	return c.doJSON(ctx, http.MethodPost, api("repos", name, "packages"), nil, body, nil)
}

func (c *Client) DeleteRepoPackageRefs(ctx context.Context, name string, refs []string) error {
	body := Entity{"PackageRefs": refs}
	return c.doJSON(ctx, http.MethodDelete, api("repos", name, "packages"), nil, body, nil)
}

func (c *Client) AddRepoFileDir(ctx context.Context, name, dir string, noRemove, forceReplace bool) error {
	query := url.Values{}
	if noRemove {
		query.Set("noRemove", "1")
	}
	if forceReplace {
		query.Set("forceReplace", "1")
	}
	return c.doJSON(ctx, http.MethodPost, api("repos", name, "file", dir), query, nil, nil)
}

func (c *Client) CreateRepo(ctx context.Context, repo Repo) error {
	return c.doJSON(ctx, http.MethodPost, api("repos"), nil, repo, nil)
}

func (c *Client) DeleteRepo(ctx context.Context, name string, force bool) error {
	query := url.Values{}
	if force {
		query.Set("force", "1")
	}
	return c.doJSON(ctx, http.MethodDelete, api("repos", name), query, nil, nil)
}

func (c *Client) ListMirrors(ctx context.Context) ([]Mirror, error) {
	var out []Mirror
	return out, c.doJSON(ctx, http.MethodGet, api("mirrors"), nil, nil, &out)
}

func (c *Client) GetMirror(ctx context.Context, name string) (Entity, error) {
	var out Entity
	return out, c.doJSON(ctx, http.MethodGet, api("mirrors", name), nil, nil, &out)
}

func (c *Client) CreateMirror(ctx context.Context, mirror Mirror) error {
	return c.doJSON(ctx, http.MethodPost, api("mirrors"), nil, mirror, nil)
}

func (c *Client) UpdateMirror(ctx context.Context, name string) error {
	return c.doJSON(ctx, http.MethodPost, api("mirrors", name, "update"), nil, nil, nil)
}

func (c *Client) DeleteMirror(ctx context.Context, name string, force bool) error {
	query := url.Values{}
	if force {
		query.Set("force", "1")
	}
	return c.doJSON(ctx, http.MethodDelete, api("mirrors", name), query, nil, nil)
}

func (c *Client) SnapshotMirror(ctx context.Context, mirrorName, snapshotName, description string) error {
	body := Entity{"Name": snapshotName}
	if description != "" {
		body["Description"] = description
	}
	return c.doJSON(ctx, http.MethodPost, api("mirrors", mirrorName, "snapshots"), nil, body, nil)
}

func (c *Client) ListSnapshots(ctx context.Context) ([]Snapshot, error) {
	var out []Snapshot
	return out, c.doJSON(ctx, http.MethodGet, api("snapshots"), nil, nil, &out)
}

func (c *Client) GetSnapshot(ctx context.Context, name string) (Entity, error) {
	var out Entity
	return out, c.doJSON(ctx, http.MethodGet, api("snapshots", name), nil, nil, &out)
}

func (c *Client) SnapshotRepo(ctx context.Context, repoName, snapshotName, description string) error {
	body := Entity{"Name": snapshotName}
	if description != "" {
		body["Description"] = description
	}
	return c.doJSON(ctx, http.MethodPost, api("repos", repoName, "snapshots"), nil, body, nil)
}

func (c *Client) DeleteSnapshot(ctx context.Context, name string, force bool) error {
	query := url.Values{}
	if force {
		query.Set("force", "1")
	}
	return c.doJSON(ctx, http.MethodDelete, api("snapshots", name), query, nil, nil)
}

func (c *Client) ListPublishes(ctx context.Context) ([]Publish, error) {
	var out []Publish
	return out, c.doJSON(ctx, http.MethodGet, api("publish"), nil, nil, &out)
}

func (c *Client) CreatePublish(ctx context.Context, prefix, distribution, sourceKind, component, sourceName, architectures string, signing PublishSigning) error {
	source := PublishedRepo{Name: sourceName, Component: component}
	body := Entity{
		"SourceKind":   sourceKind,
		"Sources":      []PublishedRepo{source},
		"Distribution": distribution,
		"Signing":      signingBody(signing),
	}
	if arch := splitCSV(architectures); len(arch) > 0 {
		body["Architectures"] = arch
	}
	return c.doJSON(ctx, http.MethodPost, api("publish", prefix), nil, body, nil)
}

func (c *Client) SwitchPublish(ctx context.Context, prefix, distribution, component, snapshot string, signing PublishSigning) error {
	body := Entity{
		"Snapshots": []PublishedRepo{{Name: snapshot, Component: component}},
		"Signing":   signingBody(signing),
	}
	return c.doJSON(ctx, http.MethodPut, api("publish", prefix, distribution), nil, body, nil)
}

func signingBody(signing PublishSigning) Entity {
	body := Entity{
		"Skip":  signing.Skip,
		"Batch": signing.Batch,
	}
	if signing.GpgKey != "" {
		body["GpgKey"] = signing.GpgKey
	}
	if signing.Keyring != "" {
		body["Keyring"] = signing.Keyring
	}
	if signing.SecretKeyring != "" {
		body["SecretKeyring"] = signing.SecretKeyring
	}
	if signing.PassphraseFile != "" {
		body["PassphraseFile"] = signing.PassphraseFile
	}
	return body
}

func (c *Client) DropPublish(ctx context.Context, prefix, distribution string, force bool) error {
	query := url.Values{}
	if force {
		query.Set("force", "1")
	}
	return c.doJSON(ctx, http.MethodDelete, api("publish", prefix, distribution), query, nil, nil)
}

func (c *Client) ListFiles(ctx context.Context) ([]string, error) {
	var out []string
	return out, c.doJSON(ctx, http.MethodGet, api("files"), nil, nil, &out)
}

func (c *Client) ListTasks(ctx context.Context) ([]Task, error) {
	var out []Task
	return out, c.doJSON(ctx, http.MethodGet, api("tasks"), nil, nil, &out)
}
