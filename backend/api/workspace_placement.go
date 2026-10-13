package api

import (
 "context"
 "log"
 "time"

 "ray-train-platform-backend/domain"
 "ray-train-platform-backend/k8s"
)

// workspaceWorkerNodePreference shares training's global capacity accounting.
// It is queried only while launching a new workspace under its lifecycle lock;
// a failed observation removes the soft hint and never blocks debugging.
func (h *Handler) workspaceWorkerNodePreference(ctx context.Context, workspace domain.DevWorkspace, accelerator domain.AcceleratorClass, nodeSelector map[string]string) []string {
 if h == nil || h.kubernetes == nil || workspace.GPUCount <= 0 || workspace.State != domain.WorkspaceSubmitted { return nil }
 queryCtx,cancel := context.WithTimeout(ctx,2*time.Second)
 defer cancel()
 nodes,err := h.kubernetes.PreferredTeamWorkerNodes(queryCtx,k8s.TeamWorkerNodePreferenceRequest{
  TenantID:workspace.TenantID,AcceleratorClass:accelerator,
  NodeSelector:nodeSelector,DedicatedNodes:h.trainingDedicatedNodes,
  Resources:domain.Resources{WorkerReplicas:1,GPUsPerWorker:workspace.GPUCount,CPUPerWorker:4,MemoryPerWorker:"16Gi"},
 })
 if err != nil {
  log.Printf("compact node preference skipped for workspace %s: %v",workspace.ID,err)
  return nil
 }
 return nodes
}
