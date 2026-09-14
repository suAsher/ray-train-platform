package functionwarehouse

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"
)

// CreateVersion issues exactly one POST after fresh permission and model scope
// checks. Independent calls intentionally create independent versions. Callers
// must reconcile ErrUnknownOutcome before retrying the same operation.
func (c *Client) CreateVersion(ctx context.Context, token string, input CreateVersionRequest) (Version, error) {
	if !validCreate(input) { return Version{}, ErrInvalid }
	ctx, cancel := context.WithTimeout(ctx, c.requestTimeout)
	defer cancel()
	warehouse, err := c.GetWarehouse(ctx, token, input.FunctionWarehouseID)
	if err != nil { return Version{}, err }
	if !canEdit(warehouse.PermissionCodes) { return Version{}, ErrForbidden }
	models, err := c.ListModelTypes(ctx, token, input.FunctionWarehouseID)
	if err != nil { return Version{}, err }
	owned := false
	for _, model := range models { if model.ID == input.ModelTypeID { owned = true } }
	if !owned { return Version{}, ErrForbidden }
	payload := struct {
		CreateVersionRequest
		GroupID string `json:"groupId"`
		Production bool `json:"production"`
		Recommended bool `json:"recommended"`
	}{CreateVersionRequest: input, GroupID: c.target.GroupID}
	encoded, err := json.Marshal(payload)
	if err != nil { return Version{}, ErrInvalid }
	var result Version
	if err := c.mutate(ctx, token, "/model/model/version/create", nil, "application/json", bytes.NewReader(encoded), int64(len(encoded)), &result); err != nil {
		return Version{}, err
	}
	if !c.matchesVersion(result, input) { return result, ErrUnknownOutcome }
	return result, nil
}

// VerifyVersion only reads. At most 20 pages / 2,000 versions are inspected;
// absence or a mismatch is uncertain, never permission to create a duplicate.
func (c *Client) VerifyVersion(ctx context.Context, token, warehouseID, versionID string, expected CreateVersionRequest) (Version, error) {
	if !validID(versionID) || warehouseID != expected.FunctionWarehouseID || !validCreate(expected) { return Version{}, ErrInvalid }
	ctx, cancel := context.WithTimeout(ctx, c.requestTimeout)
	defer cancel()
	for pageNum := 1; pageNum <= 20; pageNum++ {
		page, err := c.ListVersions(ctx, token, warehouseID, PageQuery{PageNum: pageNum, PageSize: 100})
		if err != nil { return Version{}, err }
		for _, version := range page.Records {
			if version.ID == versionID {
				if !c.matchesVersion(version, expected) { return Version{}, ErrUnknownOutcome }
				return version, nil
			}
		}
		if len(page.Records) < 100 || int64(pageNum*100) >= page.Total { break }
	}
	return Version{}, ErrUnknownOutcome
}

func canEdit(codes []string) bool {
	for _, code := range codes { if code == "edit" || code == "admin" { return true } }
	return false
}

func validCreate(req CreateVersionRequest) bool {
	if !validID(req.FunctionWarehouseID) || !validID(req.ModelTypeID) || !validID(req.JobID) || !validID(req.RunID) || !validID(req.ExperimentID) { return false }
	if strings.TrimSpace(req.Version) == "" || len(req.Version) > 128 || !utf8.ValidString(req.Version) || strings.ContainsAny(req.Version, "\x00\r\n") || len(req.Description) > 16384 || !utf8.ValidString(req.Description) || strings.ContainsRune(req.Description, 0) { return false }
	if len(req.Paths) == 0 || len(req.Paths) > 100 { return false }
	for _, file := range req.Paths { if !validUploadedFile(file) { return false } }
	return true
}

func (c *Client) matchesVersion(version Version, expected CreateVersionRequest) bool {
	if !validID(version.ID) || version.GroupID != c.target.GroupID || version.FunctionWarehouseID != expected.FunctionWarehouseID || version.ModelTypeID != expected.ModelTypeID || version.Version != expected.Version || version.JobID != expected.JobID || version.RunID != expected.RunID || version.ExperimentID != expected.ExperimentID { return false }
	if version.Production == nil || *version.Production || len(version.Files) != len(expected.Paths) { return false }
	counts := make(map[UploadedFile]int, len(expected.Paths))
	for _, file := range expected.Paths { counts[file]++ }
	for _, file := range version.Files {
		if !validUploadedFile(file) || counts[file] == 0 { return false }
		counts[file]--
	}
	return true
}

// mutate never retries. Once sent, anything other than an explicit refusal or a
// valid success envelope is an unknown outcome. Upstream text is never exposed.
func (c *Client) mutate(ctx context.Context, token, path string, params url.Values, contentType string, body io.Reader, length int64, target any) error {
	if !validToken(token) { return ErrUnauthorized }
	address := c.baseURL + path
	if len(params) > 0 { address += "?" + params.Encode() }
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, address, body)
	if err != nil { return ErrInvalid }
	request.Header.Set("Authorization", "Bearer " + token)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", contentType)
	request.ContentLength = length
	// The immutable client copy preserves pooling and redirect refusal, while the
	// operation context controls longer streaming uploads instead of a 30s timer.
	client := *c.http
	client.Timeout = 0
	response, err := client.Do(request)
	if err != nil { return &Error{Kind: ErrUnknownOutcome} }
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 { return mutationError(response.StatusCode, response.StatusCode, response.Header.Get("Retry-After")) }
	data, err := io.ReadAll(io.LimitReader(response.Body, c.maxResponseBytes+1))
	if err != nil || int64(len(data)) > c.maxResponseBytes { return &Error{Kind: ErrUnknownOutcome, StatusCode: response.StatusCode} }
	var envelope struct {
		Code *int `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	if json.Unmarshal(data, &envelope) != nil || envelope.Code == nil { return &Error{Kind: ErrUnknownOutcome, StatusCode: response.StatusCode} }
	if *envelope.Code != 0 { return mutationError(*envelope.Code, response.StatusCode, response.Header.Get("Retry-After")) }
	if target != nil && (len(envelope.Data) == 0 || json.Unmarshal(envelope.Data, target) != nil) { return &Error{Kind: ErrUnknownOutcome, StatusCode: response.StatusCode} }
	return nil
}

func mutationError(code, status int, retryAfter string) error {
	switch code {
	case 401, 403, 429:
		return responseError(code, status, retryAfter)
	case 400, 404, 405, 409, 413, 415, 422:
		return &Error{Kind: ErrInvalid, StatusCode: status}
	default:
		return &Error{Kind: ErrUnknownOutcome, StatusCode: status}
	}
}
