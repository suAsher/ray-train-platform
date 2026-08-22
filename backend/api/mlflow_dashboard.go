package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/repositories"
)

const (
	mlflowDashboardCookieName  = "ray_mlflow_dashboard"
	mlflowDashboardBasePath    = "/mlflow/"
	mlflowDashboardTicketSize  = 32
	mlflowDashboardAllow       = "GET, HEAD, OPTIONS, POST, PUT, PATCH, DELETE"
	mlflowDashboardRewriteMax  = 2 << 20
	mlflowDashboardTicketOIDC  = byte('O')
	mlflowDashboardTicketLocal = byte('L')
	mlflowDashboardTicketDemo  = byte('D')
)

type mlflowDashboardPassThroughBody struct {
	reader io.Reader
	closer io.Closer
}

func (b *mlflowDashboardPassThroughBody) Read(p []byte) (int, error) {
	return b.reader.Read(p)
}

func (b *mlflowDashboardPassThroughBody) Close() error {
	return b.closer.Close()
}

type MLflowDashboardStore interface {
	CreateMLflowDashboardTicket(context.Context, repositories.MLflowDashboardTicketRecord) error
	ConsumeMLflowDashboardTicket(context.Context, string, time.Time) (repositories.MLflowDashboardTicketRecord, error)
	AuthorizeMLflowDashboardPrincipal(context.Context, auth.Principal) (bool, error)
	CreateMLflowAuditLog(context.Context, repositories.MLflowAuditEvent) error
}

var mlflowDashboardTransport = func() http.RoundTripper {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DisableCompression = true
	return transport
}()

func (h *Handler) RegisterMLflowDashboardAccessRoute(group *gin.RouterGroup) {
	if h.mlflowDashboardEnabled {
		group.POST("/mlflow-dashboard-access", h.issueMLflowDashboardAccess)
	}
}

func (h *Handler) RegisterMLflowDashboardProxyRoute(group *gin.RouterGroup) {
	if h.mlflowDashboardEnabled {
		group.Any("/mlflow/*path", h.proxyMLflowDashboard)
	}
}

func (h *Handler) issueMLflowDashboardAccess(c *gin.Context) {
	principal, ok := h.principal(c)
	if !ok {
		h.writeError(c, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication is required")
		return
	}
	if h.mlflowDashboardStore == nil {
		h.writeError(c, http.StatusServiceUnavailable, "MLFLOW_DASHBOARD_UNAVAILABLE", "MLflow Dashboard access is not configured")
		return
	}
	authMarker, ok := mlflowDashboardAuthMarker(principal.AuthType)
	if !ok {
		h.writeError(c, http.StatusForbidden, "INTERACTIVE_LOGIN_REQUIRED", "an interactive user login is required")
		return
	}
	randomTicket := make([]byte, mlflowDashboardTicketSize)
	if _, err := io.ReadFull(h.mlflowDashboardRandom, randomTicket); err != nil {
		h.writeError(c, http.StatusInternalServerError, "MLFLOW_DASHBOARD_ACCESS_FAILED", "could not issue MLflow Dashboard access")
		return
	}
	// Bind the opaque one-time ticket to its interactive authentication source.
	// The remaining 31 random bytes still provide 248 bits of entropy.
	randomTicket[0] = authMarker
	rawTicket := base64.RawURLEncoding.EncodeToString(randomTicket)
	tokenHash := sha256.Sum256([]byte(rawTicket))
	now := h.mlflowDashboardNow().UTC()
	record := repositories.MLflowDashboardTicketRecord{
		TokenHash: hex.EncodeToString(tokenHash[:]),
		TenantID:  principal.TenantID,
		UserID:    principal.Subject,
		CreatedAt: now,
		ExpiresAt: now.Add(domain.MLflowDashboardTicketTTL),
	}
	if err := h.mlflowDashboardStore.CreateMLflowDashboardTicket(c.Request.Context(), record); err != nil {
		h.writeError(c, http.StatusInternalServerError, "MLFLOW_DASHBOARD_ACCESS_FAILED", "could not issue MLflow Dashboard access")
		return
	}
	c.Header("Cache-Control", "no-store")
	h.writeSuccess(c, http.StatusOK, map[string]string{"url": mlflowDashboardBasePath + "?access_token=" + url.QueryEscape(rawTicket)})
}

func (h *Handler) proxyMLflowDashboard(c *gin.Context) {
	if !isMLflowDashboardMethodAllowed(c.Request.Method) {
		c.Header("Allow", mlflowDashboardAllow)
		h.writeError(c, http.StatusMethodNotAllowed, "MLFLOW_DASHBOARD_METHOD_NOT_ALLOWED", "MLflow Dashboard request method is not allowed")
		return
	}
	if _, present := c.Request.URL.Query()["access_token"]; present {
		h.exchangeMLflowDashboardTicket(c)
		return
	}
	cookie, err := c.Request.Cookie(mlflowDashboardCookieName)
	if err != nil {
		h.writeError(c, http.StatusUnauthorized, "MLFLOW_DASHBOARD_AUTH_REQUIRED", "open MLflow Dashboard from the authenticated application")
		return
	}
	claims, err := domain.VerifyMLflowDashboardSessionClaims(cookie.Value, h.mlflowDashboardPepper, h.mlflowDashboardNow())
	if err != nil {
		h.clearMLflowDashboardCookie(c)
		h.writeError(c, http.StatusUnauthorized, "MLFLOW_DASHBOARD_AUTH_REQUIRED", "open MLflow Dashboard from the authenticated application")
		return
	}

	startedAt := h.mlflowDashboardNow()
	defer h.auditMLflowDashboardRequest(c, claims, startedAt)
	if !h.authorizeMLflowDashboardClaims(c, claims) {
		return
	}
	if isMLflowMutation(c.Request.Method) && !h.hasValidMLflowMutationOrigin(c.Request) {
		h.writeError(c, http.StatusForbidden, "MLFLOW_DASHBOARD_ORIGIN_FORBIDDEN", "MLflow Dashboard mutation origin is not allowed")
		return
	}
	target, err := url.Parse(h.mlflowTrackingURL)
	if err != nil || target.Scheme == "" || target.Host == "" {
		h.writeError(c, http.StatusBadGateway, "MLFLOW_DASHBOARD_UPSTREAM_INVALID", "MLflow Dashboard upstream is invalid")
		return
	}
	h.serveMLflowDashboardProxy(c, target)
}

func mlflowDashboardAuthMarker(authType auth.AuthenticationType) (byte, bool) {
	switch authType {
	case auth.AuthTypeOIDC:
		return mlflowDashboardTicketOIDC, true
	case auth.AuthTypeLocal:
		return mlflowDashboardTicketLocal, true
	case auth.AuthTypeDemo:
		return mlflowDashboardTicketDemo, true
	default:
		return 0, false
	}
}

func mlflowDashboardAuthType(marker byte) (auth.AuthenticationType, bool) {
	switch marker {
	case mlflowDashboardTicketOIDC:
		return auth.AuthTypeOIDC, true
	case mlflowDashboardTicketLocal:
		return auth.AuthTypeLocal, true
	case mlflowDashboardTicketDemo:
		return auth.AuthTypeDemo, true
	default:
		return "", false
	}
}

func mlflowDashboardSessionNonce(authType auth.AuthenticationType, tokenHash string) string {
	return string(authType) + ":" + tokenHash
}

func parseMLflowDashboardSessionNonce(nonce string) (auth.AuthenticationType, string, bool) {
	authTypeText, tokenHash, found := strings.Cut(nonce, ":")
	if !found || len(tokenHash) != sha256.Size*2 || strings.ToLower(tokenHash) != tokenHash {
		return "", "", false
	}
	decoded, err := hex.DecodeString(tokenHash)
	if err != nil || len(decoded) != sha256.Size {
		return "", "", false
	}
	authType := auth.AuthenticationType(authTypeText)
	if _, ok := mlflowDashboardAuthMarker(authType); !ok {
		return "", "", false
	}
	return authType, tokenHash, true
}

func (h *Handler) authorizeMLflowDashboardClaims(c *gin.Context, claims domain.MLflowDashboardSessionClaims) bool {
	authType, _, ok := parseMLflowDashboardSessionNonce(claims.Nonce)
	if !ok {
		h.clearMLflowDashboardCookie(c)
		h.writeError(c, http.StatusUnauthorized, "MLFLOW_DASHBOARD_AUTH_REQUIRED", "open MLflow Dashboard from the authenticated application")
		return false
	}
	if h.mlflowDashboardStore == nil {
		h.writeError(c, http.StatusServiceUnavailable, "MLFLOW_DASHBOARD_UNAVAILABLE", "MLflow Dashboard access is not configured")
		return false
	}
	allowed, err := h.mlflowDashboardStore.AuthorizeMLflowDashboardPrincipal(c.Request.Context(), auth.Principal{
		Subject: claims.Subject, Username: claims.Subject, TenantID: claims.TenantID, AuthType: authType,
	})
	if err != nil {
		h.writeError(c, http.StatusServiceUnavailable, "MLFLOW_DASHBOARD_AUTH_UNAVAILABLE", "could not verify MLflow Dashboard access")
		return false
	}
	if !allowed {
		h.clearMLflowDashboardCookie(c)
		h.writeError(c, http.StatusUnauthorized, "MLFLOW_DASHBOARD_ACCESS_REVOKED", "MLflow Dashboard access is no longer authorized")
		return false
	}
	return true
}

func (h *Handler) clearMLflowDashboardCookie(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	http.SetCookie(c.Writer, &http.Cookie{
		Name: mlflowDashboardCookieName, Value: "", Path: mlflowDashboardBasePath,
		MaxAge: -1, Expires: time.Unix(1, 0).UTC(), HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode,
	})
}

func isMLflowDashboardMethodAllowed(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

func isMLflowMutation(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

func (h *Handler) hasValidMLflowMutationOrigin(request *http.Request) bool {
	origins := request.Header.Values("Origin")
	if len(origins) > 0 {
		return len(origins) == 1 && origins[0] == h.mlflowPublicOrigin
	}
	referers := request.Header.Values("Referer")
	if len(referers) != 1 {
		return false
	}
	referer, err := url.Parse(referers[0])
	if err != nil || referer.Scheme == "" || referer.Host == "" || referer.User != nil {
		return false
	}
	return referer.Scheme+"://"+referer.Host == h.mlflowPublicOrigin
}

func (h *Handler) serveMLflowDashboardProxy(c *gin.Context, target *url.URL) {
	publicOrigin, err := url.Parse(h.mlflowPublicOrigin)
	if err != nil || publicOrigin.Scheme == "" || publicOrigin.Host == "" {
		h.writeError(c, http.StatusBadGateway, "MLFLOW_DASHBOARD_PUBLIC_ORIGIN_INVALID", "MLflow Dashboard public origin is invalid")
		return
	}
	// MLFLOW_TRACKING_URL includes /mlflow so the control-plane API client can
	// reach a server started with --static-prefix=/mlflow. Browser requests
	// already arrive under /mlflow/, therefore the reverse proxy must use only
	// the target origin or net/http would join the prefix twice.
	proxyTarget := *target
	proxyTarget.Path = ""
	proxyTarget.RawPath = ""
	proxy := httputil.NewSingleHostReverseProxy(&proxyTarget)
	proxy.Transport = mlflowDashboardTransport
	originalDirector := proxy.Director
	proxy.Director = func(request *http.Request) {
		originalDirector(request)
		request.Host = target.Host
		for _, header := range []string{
			"Accept-Encoding",
			"Authorization",
			"Cookie",
			"Forwarded",
			"X-Forwarded-Access-Token",
			"X-Forwarded-For",
			"X-Forwarded-Host",
			"X-Forwarded-Proto",
			"X-Real-IP",
		} {
			request.Header.Del(header)
		}
		request.Header.Set("X-Forwarded-Host", publicOrigin.Host)
		request.Header.Set("X-Forwarded-Proto", publicOrigin.Scheme)
	}
	proxy.ModifyResponse = func(response *http.Response) error {
		response.Header.Del("Set-Cookie")
		setMLflowDashboardSecurityHeaders(response.Header)
		if err := rewriteMLflowDashboardLocation(response); err != nil {
			return err
		}
		return rewriteMLflowDashboardTextResponse(response)
	}
	proxy.ErrorHandler = func(_ http.ResponseWriter, _ *http.Request, _ error) {
		h.writeError(c, http.StatusBadGateway, "MLFLOW_DASHBOARD_PROXY_FAILED", "MLflow Dashboard is temporarily unavailable")
	}
	proxy.ServeHTTP(c.Writer, c.Request)
}

func setMLflowDashboardSecurityHeaders(header http.Header) {
	header.Set("Content-Security-Policy", "frame-ancestors 'none'")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("X-Frame-Options", "DENY")
}

func rewriteMLflowDashboardLocation(response *http.Response) error {
	rawLocation := response.Header.Get("Location")
	if rawLocation == "" {
		return nil
	}
	location, err := url.Parse(rawLocation)
	if err != nil {
		return fmt.Errorf("parse MLflow redirect location")
	}
	resolved := response.Request.URL.ResolveReference(location)
	redirectPath := resolved.Path
	if redirectPath == "" || redirectPath == "/" {
		redirectPath = mlflowDashboardBasePath
	} else if redirectPath == strings.TrimSuffix(mlflowDashboardBasePath, "/") {
		redirectPath = mlflowDashboardBasePath
	} else if !strings.HasPrefix(redirectPath, mlflowDashboardBasePath) {
		redirectPath = strings.TrimSuffix(mlflowDashboardBasePath, "/") + "/" + strings.TrimLeft(redirectPath, "/")
	}
	cleanLocation := &url.URL{Path: redirectPath, RawQuery: resolved.RawQuery, Fragment: resolved.Fragment}
	response.Header.Set("Location", cleanLocation.String())
	return nil
}

func rewriteMLflowDashboardTextResponse(response *http.Response) error {
	if response.Request.Method == http.MethodHead || strings.TrimSpace(response.Header.Get("Content-Encoding")) != "" {
		return nil
	}
	if isMLflowDashboardArtifactPath(response.Request.URL.Path) {
		return nil
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil {
		return nil
	}
	mediaType = strings.ToLower(mediaType)
	if mediaType != "text/html" && !strings.Contains(mediaType, "javascript") {
		return nil
	}
	originalBody := response.Body
	body, err := io.ReadAll(io.LimitReader(originalBody, mlflowDashboardRewriteMax+1))
	if err != nil {
		return err
	}
	if len(body) > mlflowDashboardRewriteMax {
		response.Body = &mlflowDashboardPassThroughBody{
			reader: io.MultiReader(bytes.NewReader(body), originalBody),
			closer: originalBody,
		}
		return nil
	}
	_ = originalBody.Close()
	for _, quote := range []string{`"`, `'`, "`"} {
		body = bytes.ReplaceAll(body, []byte(quote+"/ajax-api/"), []byte(quote+"/mlflow/ajax-api/"))
		body = bytes.ReplaceAll(body, []byte(quote+"/api/"), []byte(quote+"/mlflow/api/"))
	}
	response.Body = io.NopCloser(bytes.NewReader(body))
	response.ContentLength = int64(len(body))
	response.Header.Set("Content-Length", strconv.Itoa(len(body)))
	return nil
}

func isMLflowDashboardArtifactPath(path string) bool {
	if path == "/mlflow-artifacts" || strings.HasPrefix(path, "/mlflow-artifacts/") {
		return true
	}
	path = strings.TrimPrefix(path, strings.TrimSuffix(mlflowDashboardBasePath, "/"))
	return path == "/mlflow-artifacts" || strings.HasPrefix(path, "/mlflow-artifacts/")
}

func (h *Handler) auditMLflowDashboardRequest(c *gin.Context, claims domain.MLflowDashboardSessionClaims, startedAt time.Time) {
	if h.mlflowDashboardStore == nil {
		return
	}
	duration := h.mlflowDashboardNow().Sub(startedAt)
	event := repositories.MLflowAuditEvent{
		Principal: auth.Principal{
			TenantID: claims.TenantID, Subject: claims.Subject, Username: claims.Subject,
			AuthType: auth.AuthenticationType("mlflow-session"),
		},
		Method: c.Request.Method, Path: c.Request.URL.Path, Status: c.Writer.Status(),
		Duration: duration, RequestID: c.GetHeader("X-Request-ID"),
	}
	if err := h.mlflowDashboardStore.CreateMLflowAuditLog(c.Request.Context(), event); err != nil {
		log.Printf("create MLflow Dashboard audit log: %v", err)
	}
}

func (h *Handler) exchangeMLflowDashboardTicket(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	rawTicket := c.Query("access_token")
	decoded, err := base64.RawURLEncoding.DecodeString(rawTicket)
	if err != nil || len(decoded) != mlflowDashboardTicketSize || base64.RawURLEncoding.EncodeToString(decoded) != rawTicket || h.mlflowDashboardStore == nil {
		h.writeError(c, http.StatusUnauthorized, "MLFLOW_DASHBOARD_TICKET_INVALID", "MLflow Dashboard access link is invalid or expired")
		return
	}
	authType, ok := mlflowDashboardAuthType(decoded[0])
	if !ok {
		h.writeError(c, http.StatusUnauthorized, "MLFLOW_DASHBOARD_TICKET_INVALID", "MLflow Dashboard access link is invalid or expired")
		return
	}
	tokenHash := sha256.Sum256([]byte(rawTicket))
	tokenHashText := hex.EncodeToString(tokenHash[:])
	now := h.mlflowDashboardNow().UTC()
	record, err := h.mlflowDashboardStore.ConsumeMLflowDashboardTicket(c.Request.Context(), tokenHashText, now)
	if err != nil {
		h.writeError(c, http.StatusUnauthorized, "MLFLOW_DASHBOARD_TICKET_INVALID", "MLflow Dashboard access link is invalid or expired")
		return
	}
	allowed, err := h.mlflowDashboardStore.AuthorizeMLflowDashboardPrincipal(c.Request.Context(), auth.Principal{
		Subject: record.UserID, Username: record.UserID, TenantID: record.TenantID, AuthType: authType,
	})
	if err != nil {
		h.writeError(c, http.StatusServiceUnavailable, "MLFLOW_DASHBOARD_AUTH_UNAVAILABLE", "could not verify MLflow Dashboard access")
		return
	}
	if !allowed {
		h.clearMLflowDashboardCookie(c)
		h.writeError(c, http.StatusUnauthorized, "MLFLOW_DASHBOARD_ACCESS_REVOKED", "MLflow Dashboard access is no longer authorized")
		return
	}
	session, err := domain.IssueMLflowDashboardSession(record.TenantID, record.UserID, mlflowDashboardSessionNonce(authType, record.TokenHash), h.mlflowDashboardPepper, now, h.mlflowDashboardTTL)
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "MLFLOW_DASHBOARD_SESSION_FAILED", "could not create MLflow Dashboard session")
		return
	}
	http.SetCookie(c.Writer, &http.Cookie{
		Name: mlflowDashboardCookieName, Value: session, Path: mlflowDashboardBasePath,
		MaxAge: int(h.mlflowDashboardTTL.Seconds()), HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode,
	})
	query := c.Request.URL.Query()
	query.Del("access_token")
	location := c.Request.URL.Path
	if encoded := query.Encode(); encoded != "" {
		location += "?" + encoded
	}
	c.Redirect(http.StatusFound, location)
}
