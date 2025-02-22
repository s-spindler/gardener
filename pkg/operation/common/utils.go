// Copyright (c) 2018 SAP SE or an SAP affiliate company. All rights reserved. This file is licensed under the Apache Software License, v. 2 except as noted otherwise in the LICENSE file
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package common

import (
	"context"
	"errors"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	gardencorev1alpha1 "github.com/gardener/gardener/pkg/apis/core/v1alpha1"
	constants "github.com/gardener/gardener/pkg/apis/core/v1alpha1/constants"
	gardenv1beta1 "github.com/gardener/gardener/pkg/apis/garden/v1beta1"
	gardenlisters "github.com/gardener/gardener/pkg/client/garden/listers/garden/v1beta1"
	"github.com/gardener/gardener/pkg/client/kubernetes"
	"github.com/gardener/gardener/pkg/utils"
	kutil "github.com/gardener/gardener/pkg/utils/kubernetes"
	"github.com/gardener/gardener/pkg/version"
	errors2 "github.com/pkg/errors"

	jsoniter "github.com/json-iterator/go"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2beta1 "k8s.io/api/autoscaling/v2beta1"
	batchv1beta1 "k8s.io/api/batch/v1beta1"
	corev1 "k8s.io/api/core/v1"
	extensionsv1beta1 "k8s.io/api/extensions/v1beta1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var json = jsoniter.ConfigFastest

// GetSecretKeysWithPrefix returns a list of keys of the given map <m> which are prefixed with <kind>.
func GetSecretKeysWithPrefix(kind string, m map[string]*corev1.Secret) []string {
	result := []string{}
	for key := range m {
		if strings.HasPrefix(key, kind) {
			result = append(result, key)
		}
	}
	return result
}

// ComputeClusterIP parses the provided <cidr> and sets the last byte to the value of <lastByte>.
// For example, <cidr> = 100.64.0.0/11 and <lastByte> = 10 the result would be 100.64.0.10
func ComputeClusterIP(cidr string, lastByte byte) string {
	ip, _, _ := net.ParseCIDR(cidr)
	ip = ip.To4()
	ip[3] = lastByte
	return ip.String()
}

// GenerateAddonConfig returns the provided <values> in case <enabled> is true. Otherwise, nil is
// being returned.
func GenerateAddonConfig(values map[string]interface{}, enabled bool) map[string]interface{} {
	v := map[string]interface{}{
		"enabled": enabled,
	}
	if enabled {
		for key, value := range values {
			v[key] = value
		}
	}
	return v
}

// GenerateTerraformVariablesEnvironment takes a <secret> and a <keyValueMap> and builds an environment which
// can be injected into the Terraformer job/pod manifest. The keys of the <keyValueMap> will be prefixed with
// 'TF_VAR_' and the value will be used to extract the respective data from the <secret>.
func GenerateTerraformVariablesEnvironment(secret *corev1.Secret, keyValueMap map[string]string) map[string]string {
	out := make(map[string]string)
	for key, value := range keyValueMap {
		out[fmt.Sprintf("TF_VAR_%s", key)] = strings.TrimSpace(string(secret.Data[value]))
	}
	return out
}

// ExtractShootName returns Shoot resource name extracted from provided <backupInfrastructureName>.
func ExtractShootName(backupInfrastructureName string) string {
	tokens := strings.Split(backupInfrastructureName, "-")
	return strings.Join(tokens[:len(tokens)-1], "-")
}

// GenerateBackupInfrastructureName returns BackupInfrastructure resource name created from provided <seedNamespace> and <shootUID>.
func GenerateBackupInfrastructureName(seedNamespace string, shootUID types.UID) string {
	// TODO: Remove this and use only "--" as separator, once we have all shoots deployed as per new naming conventions.
	if IsFollowingNewNamingConvention(seedNamespace) {
		return fmt.Sprintf("%s--%s", seedNamespace, utils.ComputeSHA1Hex([]byte(shootUID))[:5])
	}
	return fmt.Sprintf("%s-%s", seedNamespace, utils.ComputeSHA1Hex([]byte(shootUID))[:5])
}

// GenerateBackupNamespaceName returns Backup namespace name created from provided <backupInfrastructureName>.
func GenerateBackupNamespaceName(backupInfrastructureName string) string {
	return fmt.Sprintf("%s--%s", BackupNamespacePrefix, backupInfrastructureName)
}

// GenerateBackupEntryName returns BackupEntry resource name created from provided <seedNamespace> and <shootUID>.
func GenerateBackupEntryName(seedNamespace string, shootUID types.UID) string {
	return fmt.Sprintf("%s--%s", seedNamespace, shootUID)
}

// ExtractShootDetailsFromBackupEntryName returns Shoot resource technicalID its UID from provided <backupEntryName>.
func ExtractShootDetailsFromBackupEntryName(backupEntryName string) (shootTechnicalID, shootUID string) {
	tokens := strings.Split(backupEntryName, "--")
	shootUID = tokens[len(tokens)-1]
	shootTechnicalID = strings.TrimSuffix(backupEntryName, shootUID)
	shootTechnicalID = strings.TrimSuffix(shootTechnicalID, "--")
	return shootTechnicalID, shootUID
}

// IsFollowingNewNamingConvention determines whether the new naming convention followed for shoot resources.
// TODO: Remove this and use only "--" as separator, once we have all shoots deployed as per new naming conventions.
func IsFollowingNewNamingConvention(seedNamespace string) bool {
	return len(strings.Split(seedNamespace, "--")) > 2
}

// ReplaceCloudProviderConfigKey replaces a key with the new value in the given cloud provider config.
func ReplaceCloudProviderConfigKey(cloudProviderConfig, separator, key, value string) string {
	keyValueRegexp := regexp.MustCompile(fmt.Sprintf(`(\Q%s\E%s)([^\n]*)`, key, separator))
	return keyValueRegexp.ReplaceAllString(cloudProviderConfig, fmt.Sprintf(`${1}%q`, strings.Replace(value, `$`, `$$`, -1)))
}

// ProjectForNamespace returns the project object responsible for a given <namespace>. It tries to identify the project object by looking for the namespace
// name in the project statuses.
func ProjectForNamespace(projectLister gardenlisters.ProjectLister, namespaceName string) (*gardenv1beta1.Project, error) {
	projectList, err := projectLister.List(labels.Everything())
	if err != nil {
		return nil, err
	}

	for _, project := range projectList {
		if project.Spec.Namespace != nil && *project.Spec.Namespace == namespaceName {
			return project, nil
		}
	}

	return nil, apierrors.NewNotFound(gardenv1beta1.Resource("Project"), fmt.Sprintf("for namespace %s", namespaceName))
}

// ProjectNameForNamespace determines the project name for a given <namespace>. It tries to identify it first per the namespace's ownerReferences.
// If it doesn't help then it will check whether the project name is a label on the namespace object. If it doesn't help then the name can be inferred
// from the namespace name in case it is prefixed with the project prefix. If none of those approaches the namespace name itself is returned as project
// name.
func ProjectNameForNamespace(namespace *corev1.Namespace) string {
	for _, ownerReference := range namespace.OwnerReferences {
		if ownerReference.Kind == "Project" {
			return ownerReference.Name
		}
	}

	if name, ok := namespace.Labels[ProjectName]; ok {
		return name
	}

	if nameSplit := strings.Split(namespace.Name, ProjectPrefix); len(nameSplit) > 1 {
		return nameSplit[1]
	}

	return namespace.Name
}

// MergeOwnerReferences merges the newReferences with the list of existing references.
func MergeOwnerReferences(references []metav1.OwnerReference, newReferences ...metav1.OwnerReference) []metav1.OwnerReference {
	uids := make(map[types.UID]struct{})
	for _, reference := range references {
		uids[reference.UID] = struct{}{}
	}

	for _, newReference := range newReferences {
		if _, ok := uids[newReference.UID]; !ok {
			references = append(references, newReference)
		}
	}

	return references
}

// ReadLeaderElectionRecord returns the leader election record for a given lock type and a namespace/name combination.
func ReadLeaderElectionRecord(k8sClient kubernetes.Interface, lock, namespace, name string) (*resourcelock.LeaderElectionRecord, error) {
	var (
		leaderElectionRecord resourcelock.LeaderElectionRecord
		annotations          map[string]string
	)

	switch lock {
	case resourcelock.EndpointsResourceLock:
		endpoint, err := k8sClient.Kubernetes().CoreV1().Endpoints(namespace).Get(name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		annotations = endpoint.Annotations
	case resourcelock.ConfigMapsResourceLock:
		configmap, err := k8sClient.Kubernetes().CoreV1().ConfigMaps(namespace).Get(name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		annotations = configmap.Annotations
	default:
		return nil, fmt.Errorf("Unknown lock type: %s", lock)
	}

	leaderElection, ok := annotations[resourcelock.LeaderElectionRecordAnnotationKey]
	if !ok {
		return nil, fmt.Errorf("Could not find key %s in annotations", resourcelock.LeaderElectionRecordAnnotationKey)
	}

	if err := json.Unmarshal([]byte(leaderElection), &leaderElectionRecord); err != nil {
		return nil, fmt.Errorf("Failed to unmarshal leader election record: %+v", err)
	}

	return &leaderElectionRecord, nil
}

// GardenerDeletionGracePeriod is the default grace period for Gardener's force deletion methods.
var GardenerDeletionGracePeriod = 5 * time.Minute

// ShouldObjectBeRemoved determines whether the given object should be gone now.
// This is calculated by first checking the deletion timestamp of an object: If the deletion timestamp
// is unset, the object should not be removed - i.e. this returns false.
// Otherwise, it is checked whether the deletionTimestamp is before the current time minus the
// grace period.
func ShouldObjectBeRemoved(obj metav1.Object, gracePeriod time.Duration) bool {
	deletionTimestamp := obj.GetDeletionTimestamp()
	if deletionTimestamp == nil {
		return false
	}

	return deletionTimestamp.Time.Before(time.Now().Add(-gracePeriod))
}

// ComputeSecretCheckSum computes the sha256 checksum of secret data.
func ComputeSecretCheckSum(m map[string][]byte) string {
	var (
		hash string
		keys []string
	)

	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		hash += utils.ComputeSHA256Hex(m[k])
	}

	return utils.ComputeSHA256Hex([]byte(hash))
}

// DeleteHvpa delete all resources required for the HVPA in the given namespace.
func DeleteHvpa(k8sClient kubernetes.Interface, namespace string) error {
	if k8sClient == nil {
		return fmt.Errorf("require kubernetes client")
	}

	listOptions := metav1.ListOptions{
		LabelSelector: fmt.Sprintf("%s=%s", constants.GardenRole, GardenRoleHvpa),
	}

	// Delete all Crds with label "gardener.cloud/role=hvpa"
	if err := k8sClient.APIExtension().ApiextensionsV1beta1().CustomResourceDefinitions().DeleteCollection(
		&metav1.DeleteOptions{}, listOptions); err != nil && !apierrors.IsNotFound(err) {
		return err
	}

	// Delete all Deployments with label "gardener.cloud/role=hvpa"
	deletePropagation := metav1.DeletePropagationForeground
	if err := k8sClient.Kubernetes().AppsV1().Deployments(namespace).DeleteCollection(
		&metav1.DeleteOptions{
			PropagationPolicy: &deletePropagation,
		}, listOptions); err != nil && !apierrors.IsNotFound(err) {
		return err
	}

	// Delete all ClusterRoles with label "gardener.cloud/role=hvpa"
	if err := k8sClient.Kubernetes().RbacV1().ClusterRoles().DeleteCollection(
		&metav1.DeleteOptions{}, listOptions); err != nil && !apierrors.IsNotFound(err) {
		return err
	}

	// Delete all ClusterRoleBindings with label "gardener.cloud/role=hvpa"
	if err := k8sClient.Kubernetes().RbacV1().ClusterRoleBindings().DeleteCollection(
		&metav1.DeleteOptions{}, listOptions); err != nil && !apierrors.IsNotFound(err) {
		return err
	}

	// Delete all ServiceAccounts with label "gardener.cloud/role=hvpa"
	if err := k8sClient.Kubernetes().CoreV1().ServiceAccounts(namespace).DeleteCollection(
		&metav1.DeleteOptions{}, listOptions); err != nil && !apierrors.IsNotFound(err) {
		return err
	}

	return nil
}

// DeleteLoggingStack deletes all resource of the EFK logging stack in the given namespace.
func DeleteLoggingStack(ctx context.Context, k8sClient client.Client, namespace string) error {
	if k8sClient == nil {
		return errors.New("must provide non-nil kubernetes client to common.DeleteLoggingStack")
	}

	// Delete the resources below that match "garden.sapcloud.io/role=logging"
	lists := []runtime.Object{
		&corev1.ConfigMapList{},
		&batchv1beta1.CronJobList{},
		&rbacv1.ClusterRoleList{},
		&rbacv1.ClusterRoleBindingList{},
		&appsv1.DaemonSetList{},
		&appsv1.DeploymentList{},
		&autoscalingv2beta1.HorizontalPodAutoscalerList{},
		&extensionsv1beta1.IngressList{},
		&corev1.SecretList{},
		&corev1.ServiceAccountList{},
		&corev1.ServiceList{},
		&appsv1.StatefulSetList{},
	}

	// TODO: Use `DeleteCollection` as soon it is in the controller-runtime:
	// https://github.com/kubernetes-sigs/controller-runtime/pull/324

	for _, list := range lists {
		if err := k8sClient.List(ctx, list,
			client.InNamespace(namespace),
			client.MatchingLabels(map[string]string{GardenRole: GardenRoleLogging})); err != nil {
			return err
		}

		if err := meta.EachListItem(list, func(obj runtime.Object) error {
			return client.IgnoreNotFound(k8sClient.Delete(ctx, obj, kubernetes.DefaultDeleteOptionFuncs...))
		}); err != nil {
			return err
		}
	}

	return nil
}

// DeleteAlertmanager deletes all resources of the Alertmanager in a given namespace.
func DeleteAlertmanager(ctx context.Context, k8sClient client.Client, namespace string) error {
	objs := []runtime.Object{
		&appsv1.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:      AlertManagerStatefulSetName,
				Namespace: namespace,
			},
		},
		&extensionsv1beta1.Ingress{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "alertmanager",
				Namespace: namespace,
			},
		},
		&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "alertmanager-client",
				Namespace: namespace,
			},
		},
		&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "alertmanager",
				Namespace: namespace,
			},
		},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "alertmanager-basic-auth",
				Namespace: namespace,
			},
		},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "alertmanager-tls",
				Namespace: namespace,
			},
		},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "alertmanager-config",
				Namespace: namespace,
			},
		},
	}

	for _, obj := range objs {
		if err := k8sClient.Delete(ctx, obj, kubernetes.DefaultDeleteOptionFuncs...); client.IgnoreNotFound(err) != nil {
			return err
		}
	}

	return nil
}

// DeleteGrafanaByRole deletes the monitoring stack for the shoot owner.
func DeleteGrafanaByRole(k8sClient kubernetes.Interface, namespace, role string) error {
	if k8sClient == nil {
		return fmt.Errorf("require kubernetes client")
	}

	listOptions := metav1.ListOptions{
		LabelSelector: fmt.Sprintf("%s=%s,%s=%s", "component", "grafana", "role", role),
	}

	deletePropagation := metav1.DeletePropagationForeground
	if err := k8sClient.Kubernetes().AppsV1().Deployments(namespace).DeleteCollection(
		&metav1.DeleteOptions{
			PropagationPolicy: &deletePropagation,
		}, listOptions); err != nil && !apierrors.IsNotFound(err) {
		return err
	}

	if err := k8sClient.Kubernetes().CoreV1().ConfigMaps(namespace).DeleteCollection(
		&metav1.DeleteOptions{}, listOptions); err != nil && !apierrors.IsNotFound(err) {
		return err
	}

	if err := k8sClient.Kubernetes().ExtensionsV1beta1().Ingresses(namespace).DeleteCollection(
		&metav1.DeleteOptions{}, listOptions); err != nil && !apierrors.IsNotFound(err) {
		return err
	}

	if err := k8sClient.Kubernetes().CoreV1().Secrets(namespace).DeleteCollection(
		&metav1.DeleteOptions{}, listOptions); err != nil && !apierrors.IsNotFound(err) {
		return err
	}

	if err := k8sClient.Kubernetes().CoreV1().Services(namespace).Delete(fmt.Sprintf("grafana-%s", role),
		&metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

// GetDomainInfoFromAnnotations returns the provider and the domain that is specified in the give annotations.
func GetDomainInfoFromAnnotations(annotations map[string]string) (provider string, domain string, includeZones, excludeZones []string, err error) {
	if annotations == nil {
		return "", "", nil, nil, fmt.Errorf("domain secret has no annotations")
	}

	if providerAnnotation, ok := annotations[DNSProviderDeprecated]; ok {
		provider = providerAnnotation
	}
	if providerAnnotation, ok := annotations[DNSProvider]; ok {
		provider = providerAnnotation
	}

	if domainAnnotation, ok := annotations[DNSDomainDeprecated]; ok {
		domain = domainAnnotation
	}
	if domainAnnotation, ok := annotations[DNSDomain]; ok {
		domain = domainAnnotation
	}

	if includeZonesAnnotation, ok := annotations[DNSIncludeZones]; ok {
		includeZones = strings.Split(includeZonesAnnotation, ",")
	}
	if excludeZonesAnnotation, ok := annotations[DNSExcludeZones]; ok {
		excludeZones = strings.Split(excludeZonesAnnotation, ",")
	}

	if len(domain) == 0 {
		return "", "", nil, nil, fmt.Errorf("missing dns domain annotation on domain secret")
	}
	if len(provider) == 0 {
		return "", "", nil, nil, fmt.Errorf("missing dns provider annotation on domain secret")
	}

	return
}

// CurrentReplicaCount returns the current replicaCount for the given deployment.
func CurrentReplicaCount(client client.Client, namespace, deploymentName string) (int32, error) {
	deployment := &appsv1.Deployment{}
	if err := client.Get(context.TODO(), kutil.Key(namespace, deploymentName), deployment); err != nil && !apierrors.IsNotFound(err) {
		return 0, err
	}
	if deployment.Spec.Replicas == nil {
		return 0, nil
	}
	return *deployment.Spec.Replicas, nil
}

// RespectShootSyncPeriodOverwrite checks whether to respect the sync period overwrite of a Shoot or not.
func RespectShootSyncPeriodOverwrite(respectSyncPeriodOverwrite bool, shoot *gardenv1beta1.Shoot) bool {
	return respectSyncPeriodOverwrite || shoot.Namespace == GardenNamespace
}

// ShouldIgnoreShoot determines whether a Shoot should be ignored or not.
func ShouldIgnoreShoot(respectSyncPeriodOverwrite bool, shoot *gardenv1beta1.Shoot) bool {
	if !RespectShootSyncPeriodOverwrite(respectSyncPeriodOverwrite, shoot) {
		return false
	}

	value, ok := shoot.Annotations[ShootIgnore]
	if !ok {
		return false
	}

	ignore, _ := strconv.ParseBool(value)
	return ignore
}

// IsShootFailed checks if a Shoot is failed.
func IsShootFailed(shoot *gardenv1beta1.Shoot) bool {
	lastOperation := shoot.Status.LastOperation

	return lastOperation != nil && lastOperation.State == gardencorev1alpha1.LastOperationStateFailed &&
		shoot.Generation == shoot.Status.ObservedGeneration &&
		shoot.Status.Gardener.Version == version.Get().GitVersion
}

// IsNowInEffectiveShootMaintenanceTimeWindow checks if the current time is in the effective
// maintenance time window of the Shoot.
func IsNowInEffectiveShootMaintenanceTimeWindow(shoot *gardenv1beta1.Shoot) bool {
	return EffectiveShootMaintenanceTimeWindow(shoot).Contains(time.Now())
}

// IsObservedAtLatestGenerationAndSucceeded checks whether the Shoot's generation has changed or if the LastOperation status
// is Succeeded.
func IsObservedAtLatestGenerationAndSucceeded(shoot *gardenv1beta1.Shoot) bool {
	lastOperation := shoot.Status.LastOperation
	return shoot.Generation == shoot.Status.ObservedGeneration &&
		(lastOperation != nil && lastOperation.State == gardencorev1alpha1.LastOperationStateSucceeded)
}

// SyncPeriodOfShoot determines the sync period of the given shoot.
//
// If no overwrite is allowed, the defaultMinSyncPeriod is returned.
// Otherwise, the overwrite is parsed. If an error occurs or it is smaller than the defaultMinSyncPeriod,
// the defaultMinSyncPeriod is returned. Otherwise, the overwrite is returned.
func SyncPeriodOfShoot(respectSyncPeriodOverwrite bool, defaultMinSyncPeriod time.Duration, shoot *gardenv1beta1.Shoot) time.Duration {
	if !RespectShootSyncPeriodOverwrite(respectSyncPeriodOverwrite, shoot) {
		return defaultMinSyncPeriod
	}

	syncPeriodOverwrite, ok := shoot.Annotations[ShootSyncPeriod]
	if !ok {
		return defaultMinSyncPeriod
	}

	syncPeriod, err := time.ParseDuration(syncPeriodOverwrite)
	if err != nil {
		return defaultMinSyncPeriod
	}

	if syncPeriod < defaultMinSyncPeriod {
		return defaultMinSyncPeriod
	}
	return syncPeriod
}

// EffectiveMaintenanceTimeWindow cuts a maintenance time window at the end with a guess of 15 minutes. It is subtracted from the end
// of a maintenance time window to use a best-effort kind of finishing the operation before the end.
// Generally, we can't make sure that the maintenance operation is done by the end of the time window anyway (considering large
// clusters with hundreds of nodes, a rolling update will take several hours).
func EffectiveMaintenanceTimeWindow(timeWindow *utils.MaintenanceTimeWindow) *utils.MaintenanceTimeWindow {
	return timeWindow.WithEnd(timeWindow.End().Add(0, -15, 0))
}

// EffectiveShootMaintenanceTimeWindow returns the effective MaintenanceTimeWindow of the given Shoot.
func EffectiveShootMaintenanceTimeWindow(shoot *gardenv1beta1.Shoot) *utils.MaintenanceTimeWindow {
	maintenance := shoot.Spec.Maintenance
	if maintenance == nil || maintenance.TimeWindow == nil {
		return utils.AlwaysTimeWindow
	}

	timeWindow, err := utils.ParseMaintenanceTimeWindow(maintenance.TimeWindow.Begin, maintenance.TimeWindow.End)
	if err != nil {
		return utils.AlwaysTimeWindow
	}

	return EffectiveMaintenanceTimeWindow(timeWindow)
}

// GetPersistentVolumeProvider gets the Persistent Volume Provider of seed cluster. If it is not specified, return ""
func GetPersistentVolumeProvider(seed *gardenv1beta1.Seed) string {
	if seed.Annotations == nil {
		return ""
	}
	return seed.Annotations[AnnotatePersistentVolumeProvider]
}

// GardenEtcdEncryptionSecretKey is the key to the 'backup' of the etcd encryption secret in the Garden cluster.
func GardenEtcdEncryptionSecretKey(shootNamespace, shootName string) client.ObjectKey {
	return kutil.Key(shootNamespace, fmt.Sprintf("%s.%s", shootName, EtcdEncryptionSecretName))
}

// ReadServiceAccountSigningKeySecret reads the signing key secret to extract the signing key.
// It errors if there is no value at ServiceAccountSigningKeySecretDataKey.
func ReadServiceAccountSigningKeySecret(secret *corev1.Secret) (string, error) {
	data, ok := secret.Data[ServiceAccountSigningKeySecretDataKey]
	if !ok {
		return "", fmt.Errorf("no signing key secret in secret %s/%s at .Data.%s", secret.Namespace, secret.Name, ServiceAccountSigningKeySecretDataKey)
	}

	return string(data), nil
}

// GetServiceAccountSigningKeySecret gets the signing key from the secret with the given name and namespace.
func GetServiceAccountSigningKeySecret(ctx context.Context, c client.Client, shootNamespace, secretName string) (string, error) {
	secret := &corev1.Secret{}
	if err := c.Get(ctx, kutil.Key(shootNamespace, secretName), secret); err != nil {
		return "", err
	}

	return ReadServiceAccountSigningKeySecret(secret)
}

// GetAPIServerDomain returns the fully qualified domain name of for the api-server for the Shoot cluster. The
// end result is 'api.<domain>'.
func GetAPIServerDomain(domain string) string {
	return fmt.Sprintf("%s.%s", APIServerPrefix, domain)
}

// GetSecretFromSecretRef gets the Secret object from <secretRef>.
func GetSecretFromSecretRef(ctx context.Context, c client.Client, secretRef *corev1.SecretReference) (*corev1.Secret, error) {
	secret := &corev1.Secret{}
	if err := c.Get(ctx, kutil.Key(secretRef.Namespace, secretRef.Name), secret); err != nil {
		return nil, err
	}
	return secret, nil
}

// WrapWithLastError is wrapper function for gardencorev1alpha1.LastError
func WrapWithLastError(err error, lastError *gardencorev1alpha1.LastError) error {
	if err == nil || lastError == nil {
		return err
	}
	return errors2.Wrapf(err, "last error: %s", lastError.Description)
}
