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

package shoot

import (
	"context"
	"fmt"

	"github.com/gardener/gardener/pkg/api"
	v1alpha1constants "github.com/gardener/gardener/pkg/apis/core/v1alpha1/constants"
	"github.com/gardener/gardener/pkg/apis/garden"
	"github.com/gardener/gardener/pkg/apis/garden/validation"
	"github.com/gardener/gardener/pkg/operation/common"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/apiserver/pkg/registry/generic"
	"k8s.io/apiserver/pkg/registry/rest"
	"k8s.io/apiserver/pkg/storage"
	"k8s.io/apiserver/pkg/storage/names"
)

type shootStrategy struct {
	runtime.ObjectTyper
	names.NameGenerator
}

// Strategy defines the storage strategy for Shoots.
var Strategy = shootStrategy{api.Scheme, names.SimpleNameGenerator}

// Strategy should implement rest.RESTCreateUpdateStrategy
var _ rest.RESTCreateUpdateStrategy = Strategy

func (shootStrategy) NamespaceScoped() bool {
	return true
}

func (shootStrategy) PrepareForCreate(ctx context.Context, obj runtime.Object) {
	shoot := obj.(*garden.Shoot)

	shoot.Generation = 1
	shoot.Status = garden.ShootStatus{}
}

func (shootStrategy) PrepareForUpdate(ctx context.Context, obj, old runtime.Object) {
	newShoot := obj.(*garden.Shoot)
	oldShoot := old.(*garden.Shoot)
	newShoot.Status = oldShoot.Status

	if mustIncreaseGeneration(oldShoot, newShoot) {
		newShoot.Generation = oldShoot.Generation + 1
	}

	// TODO: (mvladev) there was a bug in the validation of GCP & openstack
	// and clusters with more than one Worker network were allowed to
	// be created.
	// This is used to fix the broken Shoots on update.
	// Remove those 2 checks after several releases.
	if gcp := newShoot.Spec.Cloud.GCP; gcp != nil {
		if len(gcp.Networks.Workers) > 1 {
			gcp.Networks.Workers = []string{gcp.Networks.Workers[0]}
		}
	}

	if gcp := oldShoot.Spec.Cloud.GCP; gcp != nil {
		if len(gcp.Networks.Workers) > 1 {
			gcp.Networks.Workers = []string{gcp.Networks.Workers[0]}
		}
	}

	if openstack := newShoot.Spec.Cloud.OpenStack; openstack != nil {
		if len(openstack.Networks.Workers) > 1 {
			openstack.Networks.Workers = []string{openstack.Networks.Workers[0]}
		}
	}

	if openstack := oldShoot.Spec.Cloud.OpenStack; openstack != nil {
		if len(openstack.Networks.Workers) > 1 {
			openstack.Networks.Workers = []string{openstack.Networks.Workers[0]}
		}
	}

}

func mustIncreaseGeneration(oldShoot, newShoot *garden.Shoot) bool {
	var (
		oldPurpose, newPurpose string
	)

	// The Shoot specification changes.
	if !apiequality.Semantic.DeepEqual(oldShoot.Spec, newShoot.Spec) {
		return true
	}

	// The deletion timestamp is set.
	if oldShoot.DeletionTimestamp == nil && newShoot.DeletionTimestamp != nil {
		return true
	}

	if lastOperation := newShoot.Status.LastOperation; lastOperation != nil {
		mustIncrease := false

		switch lastOperation.State {
		case garden.LastOperationStateFailed:
			// The shoot state is failed and the retry annotation is set.
			if val, ok := newShoot.Annotations[common.ShootOperation]; ok && val == common.ShootOperationRetry {
				mustIncrease = true
			}
		default:
			// The shoot state is not failed and the reconcile or rotate-credentials annotation is set.
			if val, ok := newShoot.Annotations[common.ShootOperation]; ok {
				if val == common.ShootOperationReconcile {
					mustIncrease = true
				}
				if val == common.ShootOperationRotateKubeconfigCredentials {
					// We don't want to remove the annotation so that the controller-manager can pick it up and rotate
					// the credentials. It has to remove the annotation after it is done.
					return true
				}
			}
		}

		if mustIncrease {
			delete(newShoot.Annotations, common.ShootOperation)
			return true
		}
	}

	oldPurpose = oldShoot.ObjectMeta.Annotations[v1alpha1constants.GardenPurpose]
	newPurpose = newShoot.ObjectMeta.Annotations[v1alpha1constants.GardenPurpose]
	if oldPurpose != newPurpose {
		return true
	}

	return false
}

func (shootStrategy) Validate(ctx context.Context, obj runtime.Object) field.ErrorList {
	shoot := obj.(*garden.Shoot)
	return validation.ValidateShoot(shoot)
}

func (shootStrategy) Canonicalize(obj runtime.Object) {
}

func (shootStrategy) AllowCreateOnUpdate() bool {
	return false
}

func (shootStrategy) ValidateUpdate(ctx context.Context, newObj, oldObj runtime.Object) field.ErrorList {
	newShoot := newObj.(*garden.Shoot)
	oldShoot := oldObj.(*garden.Shoot)
	return validation.ValidateShootUpdate(newShoot, oldShoot)
}

func (shootStrategy) AllowUnconditionalUpdate() bool {
	return false
}

type shootStatusStrategy struct {
	shootStrategy
}

// StatusStrategy defines the storage strategy for the status subresource of Shoots.
var StatusStrategy = shootStatusStrategy{Strategy}

func (shootStatusStrategy) PrepareForUpdate(ctx context.Context, obj, old runtime.Object) {
	newShoot := obj.(*garden.Shoot)
	oldShoot := old.(*garden.Shoot)
	newShoot.Spec = oldShoot.Spec
}

func (shootStatusStrategy) ValidateUpdate(ctx context.Context, obj, old runtime.Object) field.ErrorList {
	return validation.ValidateShootStatusUpdate(obj.(*garden.Shoot).Status, old.(*garden.Shoot).Status)
}

// ToSelectableFields returns a field set that represents the object
// TODO: fields are not labels, and the validation rules for them do not apply.
func ToSelectableFields(shoot *garden.Shoot) fields.Set {
	// The purpose of allocation with a given number of elements is to reduce
	// amount of allocations needed to create the fields.Set. If you add any
	// field here or the number of object-meta related fields changes, this should
	// be adjusted.
	shootSpecificFieldsSet := make(fields.Set, 3)
	shootSpecificFieldsSet[garden.ShootSeedName] = getSeedName(shoot)
	return generic.AddObjectMetaFieldsSet(shootSpecificFieldsSet, &shoot.ObjectMeta, true)
}

// GetAttrs returns labels and fields of a given object for filtering purposes.
func GetAttrs(obj runtime.Object) (labels.Set, fields.Set, error) {
	shoot, ok := obj.(*garden.Shoot)
	if !ok {
		return nil, nil, fmt.Errorf("not a shoot")
	}
	return labels.Set(shoot.ObjectMeta.Labels), ToSelectableFields(shoot), nil
}

// SeedTriggerFunc matches correct seed when watching.
func SeedTriggerFunc(obj runtime.Object) []storage.MatchValue {
	shoot := obj.(*garden.Shoot)

	result := storage.MatchValue{IndexName: garden.ShootSeedName, Value: getSeedName(shoot)}
	return []storage.MatchValue{result}
}

// MatchShoot returns a generic matcher for a given label and field selector.
func MatchShoot(label labels.Selector, field fields.Selector) storage.SelectionPredicate {
	return storage.SelectionPredicate{
		Label:       label,
		Field:       field,
		GetAttrs:    GetAttrs,
		IndexFields: []string{garden.ShootSeedName},
	}
}

func getSeedName(shoot *garden.Shoot) string {
	if shoot.Spec.Cloud.Seed == nil {
		return ""
	}
	return *shoot.Spec.Cloud.Seed
}
