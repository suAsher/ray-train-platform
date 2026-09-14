// Package functionwarehouse reads the Portal model service using the caller's
// verified OAuth access token. Tokens are supplied per call and never retained.
package functionwarehouse

import "errors"

type Environment string

const (
	Production Environment = "production"
	Development Environment = "development"
)

type Target struct {
	Environment Environment `json:"environment"`
	BaseURL string `json:"baseUrl"`
	GroupID string `json:"groupId"`
}

// Environments returns an independent copy of the fixed, reviewed targets.
// There is intentionally no configurable URL or caller-supplied group ID.
func Environments() []Target {
	return []Target{
		{Production, "https://spiking.wellspiking.ai", "a989a8e9f2b94758952a60500ba8bb7b"},
		{Development, "https://spiking-dev.wellspiking.ai", "d49a984edd3d0648a43ab050d3cc0262"},
	}
}

type PageQuery struct {
	PageNum int
	PageSize int
	Keywords string
}

// Read models deliberately expose only discovery fields. Upstream paths,
// descriptions, report JSON, and unverified source metadata are not forwarded.
type Warehouse struct {
	ID string `json:"id"`
	GroupID string `json:"groupId"`
	Name string `json:"name"`
	Number string `json:"number,omitempty"`
	Visibility string `json:"visibility,omitempty"`
	PermissionCodes []string `json:"permissionCodes"`
}

type ModelType struct {
	ID string `json:"id"`
	Name string `json:"name"`
	GroupID string `json:"groupId,omitempty"`
	FunctionWarehouseID string `json:"functionWarehouseId,omitempty"`
}

type Version struct {
	ID string `json:"id"`
	Version string `json:"version"`
	Number string `json:"number,omitempty"`
	ModelTypeID string `json:"modelTypeId"`
	ModelTypeName string `json:"modelTypeName"`
	GroupID string `json:"groupId,omitempty"`
	FunctionWarehouseID string `json:"functionWarehouseId,omitempty"`
	Production *bool `json:"production,omitempty"`
}

type WarehousePage struct {
	Records []Warehouse `json:"records"`
	Total int64 `json:"total"`
	Current int `json:"current"`
	Size int `json:"size"`
}

type VersionPage struct {
	Records []Version `json:"records"`
	Total int64 `json:"total"`
	Current int `json:"current"`
	Size int `json:"size"`
}

var (
	ErrUnauthorized = errors.New("function warehouse authentication required")
	ErrForbidden = errors.New("function warehouse access denied")
	ErrRateLimited = errors.New("function warehouse rate limit exceeded")
	ErrInvalid = errors.New("invalid function warehouse request")
	ErrUnavailable = errors.New("function warehouse service unavailable")
	ErrUnknownOutcome = errors.New("function warehouse mutation outcome unknown")
)

// Error contains only safe classifications and a validated retry hint; it never
// wraps network errors, URLs, upstream messages, response bodies, or tokens.
type Error struct {
	Kind error
	StatusCode int
	RetryAfter string
}

func (e *Error) Error() string { return e.Kind.Error() }

func (e *Error) Unwrap() error { return e.Kind }
