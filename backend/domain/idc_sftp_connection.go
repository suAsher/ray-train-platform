package domain

import (
	"fmt"
	"strings"
)

// IDCConnectionState records whether the caller's platform-generated public
// key has been installed and verified against the deployment-owned SFTP host.
type IDCConnectionState string

const (
	IDCConnectionPending IDCConnectionState = "pending"
	IDCConnectionReady   IDCConnectionState = "ready"
	IDCConnectionFailed  IDCConnectionState = "failed"
)

// PersonalIDCConnection is private platform inventory. SecretName identifies
// a namespace-scoped Kubernetes Secret; neither that name nor any private key
// may be sent to a browser or persisted in object storage.
type PersonalIDCConnection struct {
	ID             string             `json:"id"`
	TenantID       string             `json:"tenantId"`
	UserID         string             `json:"userId"`
	RemoteUsername string             `json:"remoteUsername"`
	PublicKey      string             `json:"publicKey"`
	SecretName     string             `json:"-"`
	State          IDCConnectionState `json:"state"`
}

// PersonalIDCConnectionView is the only connection projection returned to a
// user. It intentionally omits Kubernetes Secret inventory and private key
// material while retaining the public key that must be installed remotely.
type PersonalIDCConnectionView struct {
	ID             string             `json:"id"`
	RemoteUsername string             `json:"remoteUsername"`
	PublicKey      string             `json:"publicKey"`
	State          IDCConnectionState `json:"state"`
}

func NewPersonalIDCConnection(id, tenantID, userID, remoteUsername, publicKey, secretName string) (PersonalIDCConnection, error) {
	if err := validateDataSpaceIdentity("IDC connection id", id); err != nil {
		return PersonalIDCConnection{}, err
	}
	if err := validateDataSpaceIdentity("tenant", tenantID); err != nil {
		return PersonalIDCConnection{}, err
	}
	if err := validateDataSpaceIdentity("user", userID); err != nil {
		return PersonalIDCConnection{}, err
	}
	username, err := normalizeIDCRemoteUsername(remoteUsername)
	if err != nil {
		return PersonalIDCConnection{}, err
	}
	if !strings.HasPrefix(strings.TrimSpace(publicKey), "ssh-ed25519 ") {
		return PersonalIDCConnection{}, fmt.Errorf("IDC public key must be an ssh-ed25519 public key")
	}
	if strings.TrimSpace(secretName) == "" || !dnsLabel.MatchString(secretName) {
		return PersonalIDCConnection{}, fmt.Errorf("IDC connection Secret name must be a DNS label")
	}
	return PersonalIDCConnection{
		ID: id, TenantID: tenantID, UserID: userID, RemoteUsername: username,
		PublicKey: strings.TrimSpace(publicKey), SecretName: secretName, State: IDCConnectionPending,
	}, nil
}

func (connection PersonalIDCConnection) Validate() error {
	if _, err := NewPersonalIDCConnection(connection.ID, connection.TenantID, connection.UserID, connection.RemoteUsername, connection.PublicKey, connection.SecretName); err != nil {
		return err
	}
	switch connection.State {
	case IDCConnectionPending, IDCConnectionReady, IDCConnectionFailed:
		return nil
	default:
		return fmt.Errorf("unsupported IDC connection state %q", connection.State)
	}
}

func (connection PersonalIDCConnection) PublicView() PersonalIDCConnectionView {
	return PersonalIDCConnectionView{
		ID: connection.ID, RemoteUsername: connection.RemoteUsername,
		PublicKey: connection.PublicKey, State: connection.State,
	}
}

func normalizeIDCRemoteUsername(raw string) (string, error) {
	username := strings.TrimSpace(raw)
	if username == "" {
		return "", fmt.Errorf("IDC remote username is required")
	}
	if len(username) > maximumLocalUsernameLength {
		return "", fmt.Errorf("IDC remote username must be at most %d characters", maximumLocalUsernameLength)
	}
	for _, char := range username {
		isLower := char >= 'a' && char <= 'z'
		isUpper := char >= 'A' && char <= 'Z'
		isDigit := char >= '0' && char <= '9'
		if !isLower && !isUpper && !isDigit && char != '-' && char != '_' && char != '.' {
			return "", fmt.Errorf("IDC remote username contains an unsafe character")
		}
	}
	return username, nil
}
