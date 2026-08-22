package domain

import "fmt"

// DataTransferDirection always describes a non-destructive copy between the
// caller's fixed personal IDC account and the caller's My files logical root.
type DataTransferDirection string

const (
	DataTransferIDCToTOS DataTransferDirection = "idc-to-tos"
	DataTransferTOSToIDC DataTransferDirection = "tos-to-idc"
)

// DataTransferState is durable progress collected independently from the
// short-lived Kubernetes Job that performs the copy.
type DataTransferState string

const (
	DataTransferQueued    DataTransferState = "queued"
	DataTransferRunning   DataTransferState = "running"
	DataTransferSucceeded DataTransferState = "succeeded"
	DataTransferFailed    DataTransferState = "failed"
	DataTransferCancelled DataTransferState = "cancelled"
)

type DataTransfer struct {
	ID              string                `json:"id"`
	TenantID        string                `json:"tenantId"`
	UserID          string                `json:"userId"`
	Direction       DataTransferDirection `json:"direction"`
	IDCRelativePath string                `json:"idcRelativePath"`
	TOSLocation     DataLocation          `json:"tosLocation"`
	State           DataTransferState     `json:"state"`
}

func NewDataTransfer(id, tenantID, userID string, direction DataTransferDirection, idcRelativePath string, tosLocation DataLocation) (DataTransfer, error) {
	if err := validateDataSpaceIdentity("transfer id", id); err != nil {
		return DataTransfer{}, err
	}
	if err := validateDataSpaceIdentity("tenant", tenantID); err != nil {
		return DataTransfer{}, err
	}
	if err := validateDataSpaceIdentity("user", userID); err != nil {
		return DataTransfer{}, err
	}
	if direction != DataTransferIDCToTOS && direction != DataTransferTOSToIDC {
		return DataTransfer{}, fmt.Errorf("unsupported data transfer direction %q", direction)
	}
	path, err := NormalizeStorageRelativePath(idcRelativePath)
	if err != nil || path == "" {
		if err != nil {
			return DataTransfer{}, fmt.Errorf("IDC transfer path: %w", err)
		}
		return DataTransfer{}, fmt.Errorf("IDC transfer path is required")
	}
	location, err := NewDataLocation(tosLocation.Space, tosLocation.RelativePath)
	if err != nil {
		return DataTransfer{}, fmt.Errorf("TOS transfer location: %w", err)
	}
	if location.Space != DataSpaceMyFiles {
		return DataTransfer{}, fmt.Errorf("data transfers may only use %q", DataSpaceMyFiles)
	}
	return DataTransfer{
		ID: id, TenantID: tenantID, UserID: userID, Direction: direction,
		IDCRelativePath: path, TOSLocation: location, State: DataTransferQueued,
	}, nil
}

func (transfer DataTransfer) Validate() error {
	if _, err := NewDataTransfer(transfer.ID, transfer.TenantID, transfer.UserID, transfer.Direction, transfer.IDCRelativePath, transfer.TOSLocation); err != nil {
		return err
	}
	switch transfer.State {
	case DataTransferQueued, DataTransferRunning, DataTransferSucceeded, DataTransferFailed, DataTransferCancelled:
		return nil
	default:
		return fmt.Errorf("unsupported data transfer state %q", transfer.State)
	}
}

func (transfer *DataTransfer) TransitionTo(next DataTransferState) error {
	if transfer == nil {
		return fmt.Errorf("data transfer is required")
	}
	allowed := map[DataTransferState][]DataTransferState{
		DataTransferQueued:  {DataTransferRunning, DataTransferCancelled},
		DataTransferRunning: {DataTransferSucceeded, DataTransferFailed, DataTransferCancelled},
	}
	for _, candidate := range allowed[transfer.State] {
		if next == candidate {
			transfer.State = next
			return nil
		}
	}
	return fmt.Errorf("data transfer cannot transition from %q to %q", transfer.State, next)
}
