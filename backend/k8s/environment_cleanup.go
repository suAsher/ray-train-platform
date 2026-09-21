package k8s

import (
 "context"
 "fmt"
 metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
 apierrors "k8s.io/apimachinery/pkg/api/errors"
 "k8s.io/apimachinery/pkg/labels"
 "ray-train-platform-backend/environmentbuild"
)

// Cleanup first stops only this operation's Jobs and waits until all their Pods
// disappear. Credentials and the artifact volume remain until execution stops.
func (r *EnvironmentRunner) Cleanup(ctx context.Context,b environmentbuild.Build,retainArtifact bool) error {
 if r.client==nil || r.client.kubernetes==nil || b.ID=="" {return environmentbuild.ErrInvalid}
 selector:=labels.Set{environmentManagedLabel:environmentHash(b.ID)}.AsSelector().String()
 options:=metav1.ListOptions{LabelSelector:selector}
 jobs,err:=r.client.kubernetes.BatchV1().Jobs(r.config.Namespace).List(ctx,options);if err!=nil {return err}
 for i:=range jobs.Items {
  job:=&jobs.Items[i];if !r.owned(job,b) {return fmt.Errorf("refusing to clean unmanaged environment Job")}
  deletion:=environmentDeleteOptions(job);policy:=metav1.DeletePropagationForeground;deletion.PropagationPolicy=&policy
  if err:=r.client.kubernetes.BatchV1().Jobs(r.config.Namespace).Delete(ctx,job.Name,deletion);err!=nil && !apierrors.IsNotFound(err) {return err}
 }
 pods,err:=r.client.kubernetes.CoreV1().Pods(r.config.Namespace).List(ctx,options);if err!=nil {return err}
 if len(pods.Items)>0 {return fmt.Errorf("environment execution is still stopping")}
 // Foreground deletion must be observed, not inferred from an accepted request.
 remaining,err:=r.client.kubernetes.BatchV1().Jobs(r.config.Namespace).List(ctx,options);if err!=nil {return err}
 if len(remaining.Items)>0 {return fmt.Errorf("environment Jobs are still stopping")}
 secretName:=environmentPublishSecretName(b)
 secret,err:=r.client.kubernetes.CoreV1().Secrets(r.config.Namespace).Get(ctx,secretName,metav1.GetOptions{})
 if err!=nil && !apierrors.IsNotFound(err) {return err}
 if err==nil {
  if !r.owned(secret,b) {return fmt.Errorf("refusing unmanaged environment credential")}
  if err:=r.client.kubernetes.CoreV1().Secrets(r.config.Namespace).Delete(ctx,secretName,environmentDeleteOptions(secret));err!=nil && !apierrors.IsNotFound(err) {return err}
 }
 maps,err:=r.client.kubernetes.CoreV1().ConfigMaps(r.config.Namespace).List(ctx,options);if err!=nil {return err}
 for i:=range maps.Items {cm:=&maps.Items[i];if !r.owned(cm,b) {return fmt.Errorf("refusing unmanaged environment snapshot")};if err:=r.client.kubernetes.CoreV1().ConfigMaps(r.config.Namespace).Delete(ctx,cm.Name,environmentDeleteOptions(cm));err!=nil && !apierrors.IsNotFound(err) {return err}}
 if retainArtifact {return nil}
 pvcs,err:=r.client.kubernetes.CoreV1().PersistentVolumeClaims(r.config.Namespace).List(ctx,options);if err!=nil {return err}
 for i:=range pvcs.Items {pvc:=&pvcs.Items[i];if !r.owned(pvc,b) {return fmt.Errorf("refusing unmanaged environment artifact")};if err:=r.client.kubernetes.CoreV1().PersistentVolumeClaims(r.config.Namespace).Delete(ctx,pvc.Name,environmentDeleteOptions(pvc));err!=nil && !apierrors.IsNotFound(err) {return err}}
 return nil
}
