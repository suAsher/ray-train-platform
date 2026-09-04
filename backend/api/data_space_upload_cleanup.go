package api

import (
	"context"
	"fmt"
	"log"
	"time"
)

const dataSpaceUploadCleanupInterval = 15 * time.Minute

func (h *Handler) RunDataSpaceUploadCleanup(ctx context.Context) error {
	if h.dataSpaceUploads == nil || h.dataMultipartStore == nil {
		return nil
	}
	if err := h.cleanupExpiredDataSpaceUploads(ctx, time.Now().UTC()); err != nil && ctx.Err() == nil {
		log.Printf("initial data-space upload cleanup failed: %v", err)
	}
	ticker := time.NewTicker(dataSpaceUploadCleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case now := <-ticker.C:
			if err := h.cleanupExpiredDataSpaceUploads(ctx, now.UTC()); err != nil && ctx.Err() == nil {
				log.Printf("data-space upload cleanup failed: %v", err)
			}
		}
	}
}

func (h *Handler) cleanupExpiredDataSpaceUploads(ctx context.Context, now time.Time) error {
	sessions, err := h.dataSpaceUploads.ClaimExpiredDataSpaceUploads(ctx, now, 20)
	if err != nil {
		return fmt.Errorf("claim expired data-space uploads: %w", err)
	}
	var firstErr error
	for _, session := range sessions {
		if err := h.dataMultipartStore.AbortDataMultipart(ctx, session.RootPrefix, session.RelativePath, session.ProviderID); err != nil {
			_ = h.dataSpaceUploads.FinishDataSpaceUploadAbort(context.Background(), session.ID, false)
			if firstErr == nil {
				firstErr = fmt.Errorf("abort expired upload %s: %w", session.ID, err)
			}
			continue
		}
		if err := h.dataSpaceUploads.FinishDataSpaceUploadAbort(ctx, session.ID, true); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("finish expired upload %s: %w", session.ID, err)
		}
	}
	return firstErr
}
