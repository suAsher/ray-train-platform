package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/repositories"
)

type MembershipStore interface {
	ListTenantMemberships(context.Context, string) ([]domain.TenantMembership, error)
	PutTenantMembership(context.Context, domain.TenantMembership) error
	SetActiveTenant(context.Context, string, string) error
	SetTenantMembershipStatus(context.Context, string, string, domain.MembershipStatus) error
}

type membershipReassignmentStore interface {
	ReassignActiveMembership(context.Context, string, string, string, []string, bool) error
}

type switchActiveTenantRequest struct {
	TenantID string `json:"tenantId"`
}

type putMembershipRequest struct {
	TenantID string   `json:"tenantId"`
	Roles    []string `json:"roles"`
}

type membershipStatusRequest struct {
	Status domain.MembershipStatus `json:"status"`
}

type reassignActiveMembershipRequest struct {
	ExpectedTenantID string   `json:"expectedTenantId"`
	TargetTenantID   string   `json:"targetTenantId"`
	Roles            []string `json:"roles"`
}

func (h *Handler) listOwnMemberships(c *gin.Context) {
	principal, ok := h.interactiveMembershipPrincipal(c)
	if !ok {
		return
	}
	h.writeMembershipList(c, principal.Subject)
}

func (h *Handler) switchActiveTenant(c *gin.Context) {
	principal, ok := h.interactiveMembershipPrincipal(c)
	if !ok {
		return
	}
	var request switchActiveTenantRequest
	if err := c.ShouldBindJSON(&request); err != nil || strings.TrimSpace(request.TenantID) == "" {
		h.writeError(c, http.StatusBadRequest, "INVALID_ACTIVE_TENANT", "tenantId is required")
		return
	}
	if err := h.memberships.SetActiveTenant(c.Request.Context(), principal.Subject, request.TenantID); err != nil {
		if errors.Is(err, repositories.ErrMembershipNotFound) {
			h.writeError(c, http.StatusForbidden, "MEMBERSHIP_NOT_ACTIVE", "the selected team is not an active membership")
			return
		}
		h.writeError(c, http.StatusInternalServerError, "ACTIVE_TENANT_UPDATE_FAILED", "could not switch active team")
		return
	}
	h.writeSuccess(c, http.StatusOK, map[string]string{"tenantId": strings.TrimSpace(request.TenantID)})
}

func (h *Handler) listUserMemberships(c *gin.Context) {
	principal, ok := h.adminPrincipal(c)
	if !ok {
		return
	}
	identityID := strings.TrimSpace(c.Param("id"))
	if identityID == "" {
		h.writeError(c, http.StatusBadRequest, "IDENTITY_REQUIRED", "identity id is required")
		return
	}
	items, err := h.memberships.ListTenantMemberships(c.Request.Context(), identityID)
	if err != nil {
		h.writeError(c, http.StatusNotFound, "IDENTITY_NOT_FOUND", "identity was not found")
		return
	}
	if !principal.HasRole(domain.RoleSuperAdmin) {
		visible := make([]domain.TenantMembership, 0, 1)
		for _, item := range items {
			if item.TenantID == principal.TenantID {
				visible = append(visible, item)
			}
		}
		items = visible
	}
	h.writeSuccess(c, http.StatusOK, items)
}

func (h *Handler) reassignUserActiveMembership(c *gin.Context) {
	principal, ok := h.adminPrincipal(c)
	if !ok {
		return
	}
	if !principal.HasRole(domain.RoleSuperAdmin) {
		h.writeError(c, http.StatusForbidden, "FORBIDDEN", "super administrator role is required")
		return
	}
	store, ok := h.memberships.(membershipReassignmentStore)
	if !ok {
		h.writeError(c, http.StatusServiceUnavailable, "MEMBERSHIP_UNAVAILABLE", "team reassignment is not configured")
		return
	}
	var request reassignActiveMembershipRequest
	if err := c.ShouldBindJSON(&request); err != nil || strings.TrimSpace(request.ExpectedTenantID) == "" || strings.TrimSpace(request.TargetTenantID) == "" || len(request.Roles) == 0 {
		h.writeError(c, http.StatusBadRequest, "INVALID_MEMBERSHIP_REASSIGNMENT", "expectedTenantId, targetTenantId and roles are required")
		return
	}
	for _, role := range request.Roles {
		if strings.EqualFold(strings.TrimSpace(role), domain.RoleSuperAdmin) {
			h.writeError(c, http.StatusBadRequest, "GLOBAL_ROLE_REQUIRED", "SuperAdmin cannot be assigned through a team membership")
			return
		}
	}
	err := store.ReassignActiveMembership(c.Request.Context(), strings.TrimSpace(c.Param("id")), strings.TrimSpace(request.ExpectedTenantID), strings.TrimSpace(request.TargetTenantID), append([]string(nil), request.Roles...), true)
	if errors.Is(err, repositories.ErrActiveTenantChanged) {
		h.writeError(c, http.StatusConflict, "ACTIVE_TENANT_CHANGED", "the user's active team changed; refresh and retry")
		return
	}
	if errors.Is(err, repositories.ErrLocalUserNotFound) || errors.Is(err, repositories.ErrMembershipNotFound) {
		h.writeError(c, http.StatusNotFound, "MEMBERSHIP_TARGET_NOT_FOUND", "the user or target team was not found")
		return
	}
	if err != nil {
		h.writeError(c, http.StatusBadRequest, "MEMBERSHIP_REASSIGNMENT_FAILED", err.Error())
		return
	}
	h.writeMembershipList(c, strings.TrimSpace(c.Param("id")))
}

func (h *Handler) putUserMembership(c *gin.Context) {
	principal, ok := h.adminPrincipal(c)
	if !ok {
		return
	}
	var request putMembershipRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.writeError(c, http.StatusBadRequest, "INVALID_JSON", "request body is invalid")
		return
	}
	if !principal.HasRole(domain.RoleSuperAdmin) && request.TenantID != principal.TenantID {
		h.writeError(c, http.StatusForbidden, "FORBIDDEN", "tenant administrators may only manage their active team")
		return
	}
	for _, role := range request.Roles {
		if strings.EqualFold(strings.TrimSpace(role), domain.RoleSuperAdmin) {
			h.writeError(c, http.StatusBadRequest, "GLOBAL_ROLE_REQUIRED", "SuperAdmin is a global account role and cannot be assigned through a team membership")
			return
		}
	}
	membership := domain.TenantMembership{IdentityID: strings.TrimSpace(c.Param("id")), TenantID: strings.TrimSpace(request.TenantID), Roles: append([]string(nil), request.Roles...), Status: domain.MembershipStatusActive}
	if err := h.memberships.PutTenantMembership(c.Request.Context(), membership); err != nil {
		h.writeError(c, http.StatusBadRequest, "MEMBERSHIP_UPDATE_FAILED", err.Error())
		return
	}
	h.writeSuccess(c, http.StatusOK, membership)
}

func (h *Handler) updateUserMembershipStatus(c *gin.Context) {
	principal, ok := h.adminPrincipal(c)
	if !ok {
		return
	}
	tenantID := strings.TrimSpace(c.Param("tenant"))
	if !principal.HasRole(domain.RoleSuperAdmin) && tenantID != principal.TenantID {
		h.writeError(c, http.StatusForbidden, "FORBIDDEN", "tenant administrators may only manage their active team")
		return
	}
	var request membershipStatusRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.writeError(c, http.StatusBadRequest, "INVALID_JSON", "request body is invalid")
		return
	}
	err := h.memberships.SetTenantMembershipStatus(c.Request.Context(), c.Param("id"), tenantID, request.Status)
	if errors.Is(err, repositories.ErrLastMembership) {
		h.writeError(c, http.StatusConflict, "MEMBERSHIP_REQUIRED", "the active or last membership cannot be disabled")
		return
	}
	if err != nil {
		h.writeError(c, http.StatusBadRequest, "MEMBERSHIP_UPDATE_FAILED", "could not update membership")
		return
	}
	h.writeSuccess(c, http.StatusOK, map[string]any{"tenantId": tenantID, "status": request.Status})
}

func (h *Handler) interactiveMembershipPrincipal(c *gin.Context) (auth.Principal, bool) {
	principal, ok := h.principal(c)
	if !ok {
		h.writeError(c, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication is required")
		return auth.Principal{}, false
	}
	if !auth.IsInteractiveAuthType(principal.AuthType) {
		h.writeError(c, http.StatusForbidden, "INTERACTIVE_SESSION_REQUIRED", "an interactive login is required")
		return auth.Principal{}, false
	}
	if h.memberships == nil {
		h.writeError(c, http.StatusServiceUnavailable, "MEMBERSHIP_UNAVAILABLE", "team membership service is unavailable")
		return auth.Principal{}, false
	}
	return principal, true
}

func (h *Handler) writeMembershipList(c *gin.Context, identityID string) {
	items, err := h.memberships.ListTenantMemberships(c.Request.Context(), identityID)
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "MEMBERSHIP_LIST_FAILED", "could not list team memberships")
		return
	}
	h.writeSuccess(c, http.StatusOK, items)
}
