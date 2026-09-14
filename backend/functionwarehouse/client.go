package functionwarehouse

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	defaultResponseLimit = 4 << 20
	defaultRequestTimeout = 30 * time.Second
)

type Client struct {
	target Target
	baseURL string
	http *http.Client
	requestTimeout time.Duration
	maxResponseBytes int64
}

func NewClient(environment Environment) (*Client, error) {
	for _, target := range Environments() {
		if target.Environment != environment {
			continue
		}
		transport := &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			ForceAttemptHTTP2: true,
			MaxIdleConns: 20,
			MaxIdleConnsPerHost: 8,
			MaxConnsPerHost: 8,
			IdleConnTimeout: 90 * time.Second,
			TLSHandshakeTimeout: 10 * time.Second,
			ResponseHeaderTimeout: 10 * time.Minute,
			MaxResponseHeaderBytes: 32 << 10,
		}
		return &Client{
			target: target,
			baseURL: target.BaseURL,
			http: &http.Client{
				Transport: transport,
				Timeout: defaultRequestTimeout,
				CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
			},
			requestTimeout: defaultRequestTimeout,
			maxResponseBytes: defaultResponseLimit,
		}, nil
	}
	return nil, ErrInvalid
}

func (c *Client) ListWarehouses(ctx context.Context, token string, query PageQuery) (WarehousePage, error) {
	params, err := c.pageParams(query)
	if err != nil {
		return WarehousePage{}, err
	}
	var result WarehousePage
	if err := c.get(ctx, token, "/model/functionWarehouse/page", params, &result); err != nil {
		return WarehousePage{}, err
	}
	if result.Records == nil || len(result.Records) > 100 || result.Total < 0 {
		return WarehousePage{}, ErrUnavailable
	}
	for _, row := range result.Records {
		if !validID(row.ID) {
			return WarehousePage{}, ErrUnavailable
		}
		if row.GroupID != c.target.GroupID {
			return WarehousePage{}, ErrForbidden
		}
	}
	return result, nil
}

func (c *Client) GetWarehouse(ctx context.Context, token, id string) (Warehouse, error) {
	if !validID(id) {
		return Warehouse{}, ErrInvalid
	}
	var result Warehouse
	params := url.Values{"functionWarehouseId": {id}}
	if err := c.get(ctx, token, "/model/functionWarehouse", params, &result); err != nil {
		return Warehouse{}, err
	}
	if result.ID != id || result.GroupID != c.target.GroupID {
		return Warehouse{}, ErrForbidden
	}
	if result.PermissionCodes == nil {
		result.PermissionCodes = []string{}
	}
	return result, nil
}

func (c *Client) PermissionCodes(ctx context.Context, token, id string) ([]string, error) {
	warehouse, err := c.GetWarehouse(ctx, token, id)
	if err != nil {
		return nil, err
	}
	return warehouse.PermissionCodes, nil
}

func (c *Client) ListModelTypes(ctx context.Context, token, warehouseID string) ([]ModelType, error) {
	ctx, cancel := context.WithTimeout(ctx, c.requestTimeout)
	defer cancel()
	if _, err := c.GetWarehouse(ctx, token, warehouseID); err != nil {
		return nil, err
	}
	var result []ModelType
	params := url.Values{"functionWarehouseId": {warehouseID}, "groupId": {c.target.GroupID}}
	if err := c.get(ctx, token, "/model/model/type/list", params, &result); err != nil {
		return nil, err
	}
	for _, row := range result {
		if !validID(row.ID) {
			return nil, ErrUnavailable
		}
		if !c.childScopeMatches(row.GroupID, row.FunctionWarehouseID, warehouseID) {
			return nil, ErrForbidden
		}
	}
	if result == nil {
		result = []ModelType{}
	}
	return result, nil
}

func (c *Client) ListVersions(ctx context.Context, token, warehouseID string, query PageQuery) (VersionPage, error) {
	params, err := c.pageParams(query)
	if err != nil {
		return VersionPage{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, c.requestTimeout)
	defer cancel()
	if _, err := c.GetWarehouse(ctx, token, warehouseID); err != nil {
		return VersionPage{}, err
	}
	params.Set("functionWarehouseId", warehouseID)
	var result VersionPage
	if err := c.get(ctx, token, "/model/model/version/page", params, &result); err != nil {
		return VersionPage{}, err
	}
	if result.Records == nil || len(result.Records) > 100 || result.Total < 0 {
		return VersionPage{}, ErrUnavailable
	}
	for _, row := range result.Records {
		if !validID(row.ID) {
			return VersionPage{}, ErrUnavailable
		}
		if !c.childScopeMatches(row.GroupID, row.FunctionWarehouseID, warehouseID) {
			return VersionPage{}, ErrForbidden
		}
	}
	return result, nil
}

// Child responses do not always repeat ownership fields. Their scope comes from
// the fixed query and verified warehouse; explicit conflicting fields fail shut.
func (c *Client) childScopeMatches(groupID, actualWarehouseID, warehouseID string) bool {
	return (groupID == "" || groupID == c.target.GroupID) && (actualWarehouseID == "" || actualWarehouseID == warehouseID)
}

func (c *Client) pageParams(query PageQuery) (url.Values, error) {
	if query.PageNum < 0 || query.PageNum > 1000000 || query.PageSize < 0 || query.PageSize > 100 || len(query.Keywords) > 256 || strings.ContainsAny(query.Keywords, "\x00\r\n") {
		return nil, ErrInvalid
	}
	if query.PageNum == 0 {
		query.PageNum = 1
	}
	if query.PageSize == 0 {
		query.PageSize = 20
	}
	return url.Values{
		"groupId": {c.target.GroupID},
		"pageNum": {strconv.Itoa(query.PageNum)},
		"pageSize": {strconv.Itoa(query.PageSize)},
		"keywords": {query.Keywords},
	}, nil
}

func validID(id string) bool {
	if len(id) == 0 || len(id) > 128 {
		return false
	}
	for _, char := range id {
		if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '-' || char == '_') {
			return false
		}
	}
	return true
}

func validToken(token string) bool {
	if len(token) == 0 || len(token) > 16384 {
		return false
	}
	for _, char := range token {
		if char <= ' ' || char >= 127 {
			return false
		}
	}
	return true
}

func (c *Client) get(ctx context.Context, token, path string, params url.Values, target any) error {
	if !validToken(token) {
		return ErrUnauthorized
	}
	ctx, cancel := context.WithTimeout(ctx, c.requestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path+"?"+params.Encode(), nil)
	if err != nil {
		return ErrInvalid
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return &Error{Kind: ErrUnavailable}
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return responseError(response.StatusCode, response.StatusCode, response.Header.Get("Retry-After"))
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, c.maxResponseBytes+1))
	if err != nil || int64(len(body)) > c.maxResponseBytes {
		return &Error{Kind: ErrUnavailable, StatusCode: response.StatusCode}
	}
	var envelope struct {
		Code *int `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	if json.Unmarshal(body, &envelope) != nil || envelope.Code == nil {
		return &Error{Kind: ErrUnavailable, StatusCode: response.StatusCode}
	}
	if *envelope.Code != 0 {
		return responseError(*envelope.Code, response.StatusCode, response.Header.Get("Retry-After"))
	}
	if len(envelope.Data) == 0 || string(envelope.Data) == "null" || json.Unmarshal(envelope.Data, target) != nil {
		return &Error{Kind: ErrUnavailable, StatusCode: response.StatusCode}
	}
	return nil
}

func responseError(code, status int, retryAfter string) error {
	kind := ErrUnavailable
	switch code {
	case http.StatusUnauthorized:
		kind = ErrUnauthorized
	case http.StatusForbidden:
		kind = ErrForbidden
	case http.StatusTooManyRequests:
		kind = ErrRateLimited
	}
	result := &Error{Kind: kind, StatusCode: status}
	if kind == ErrRateLimited {
		if seconds, err := strconv.Atoi(retryAfter); err == nil && seconds >= 0 && seconds <= 86400 {
			result.RetryAfter = strconv.Itoa(seconds)
		} else if when, err := http.ParseTime(retryAfter); err == nil {
			result.RetryAfter = when.UTC().Format(http.TimeFormat)
		}
	}
	return result
}
