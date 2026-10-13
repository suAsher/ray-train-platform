package k8s

import (
 "context"
 "fmt"
 "sort"
 "strings"

 "ray-train-platform-backend/domain"
)

// TeamWorkerNodePreferenceRequest describes the complete worker shape. The
// resulting order is a soft scheduling hint; dedicated-node policies remain
// required constraints in the rendered Pod template.
type TeamWorkerNodePreferenceRequest struct {
 TenantID string
 JobID string
 AcceleratorClass domain.AcceleratorClass
 NodeSelector map[string]string
 DedicatedNodes map[string][]string
 Resources domain.Resources
}

func (c *Client) PreferredTeamWorkerNodes(ctx context.Context, request TeamWorkerNodePreferenceRequest) ([]string, error) {
 if c == nil || c.kubernetes == nil {
  return nil, fmt.Errorf("Kubernetes client is not initialized")
 }
 tenantID := strings.TrimSpace(request.TenantID)
 if tenantID == "" { return nil, fmt.Errorf("tenant id is required") }
 resources := request.Resources
 // Preserve callers that predate shape-aware placement. Production callers
 // supply explicit resources matching their worker template.
 if resources.WorkerReplicas == 0 { resources.WorkerReplicas = 1 }
 if resources.GPUsPerWorker == 0 { resources.GPUsPerWorker = 1 }
 if resources.CPUPerWorker == 0 { resources.CPUPerWorker = 4 }
 if strings.TrimSpace(resources.MemoryPerWorker) == "" { resources.MemoryPerWorker = "16Gi" }
 capacity, err := c.SchedulingCapacity(ctx, SchedulingCapacityRequest{
  TenantID:tenantID, AcceleratorClass:request.AcceleratorClass,
  NodeSelector:request.NodeSelector, DedicatedNodes:request.DedicatedNodes,
  Resources:resources, ExcludeJobID:strings.TrimSpace(request.JobID),
 })
 if err != nil { return nil, fmt.Errorf("read compact placement capacity: %w",err) }
 if !capacity.Available { return nil, fmt.Errorf("compact placement capacity is unavailable") }
 candidates := make([]domain.SchedulingCapacityNode,0,len(capacity.Nodes))
 for _, node := range capacity.Nodes {
  if node.WorkerSlots > 0 { candidates = append(candidates,node) }
 }
 sort.Slice(candidates,func(i,j int) bool {
  left,right := candidates[i],candidates[j]
  leftOccupied,rightOccupied := left.AllocatedGPUs+left.ReservedGPUs > 0,right.AllocatedGPUs+right.ReservedGPUs > 0
  if leftOccupied != rightOccupied { return leftOccupied }
  if left.FreeGPUs != right.FreeGPUs { return left.FreeGPUs < right.FreeGPUs }
  if left.TeamGPUs != right.TeamGPUs { return left.TeamGPUs > right.TeamGPUs }
  return left.Name < right.Name
 })
 nodes := make([]string,0,len(candidates))
 for _, node := range candidates { nodes = append(nodes,node.Name) }
 return nodes,nil
}
