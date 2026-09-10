package domain

import (
	"fmt"
	"strings"
	"time"
)

type MembershipStatus string

const (
	MembershipStatusActive   MembershipStatus = "ACTIVE"
	MembershipStatusInactive MembershipStatus = "INACTIVE"
)

type TenantMembership struct {
	IdentityID string           `json:"identityId"`
	TenantID   string           `json:"tenantId"`
	TenantName string           `json:"tenantName"`
	Roles      []string         `json:"roles"`
	Status     MembershipStatus `json:"status"`
	Active     bool             `json:"active"`
	CreatedAt  time.Time        `json:"createdAt"`
	UpdatedAt  time.Time        `json:"updatedAt"`
}

func (membership TenantMembership) Validate() error {
	if strings.TrimSpace(membership.IdentityID) == "" || strings.TrimSpace(membership.TenantID) == "" {
		return fmt.Errorf("membership identity and tenant are required")
	}
	if membership.Status != MembershipStatusActive && membership.Status != MembershipStatusInactive {
		return fmt.Errorf("unsupported membership status %q", membership.Status)
	}
	if _, err := NormalizeRoles(membership.Roles); err != nil {
		return err
	}
	return nil
}
