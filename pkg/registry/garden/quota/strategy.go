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

package quota

import (
	"context"

	"github.com/gardener/gardener/pkg/api"
	"github.com/gardener/gardener/pkg/apis/garden"
	"github.com/gardener/gardener/pkg/apis/garden/validation"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/apiserver/pkg/storage/names"
)

type quotaStrategy struct {
	runtime.ObjectTyper
	names.NameGenerator
}

// Strategy defines the storage strategy for Quotas.
var Strategy = quotaStrategy{api.Scheme, names.SimpleNameGenerator}

func (quotaStrategy) NamespaceScoped() bool {
	return true
}

func (quotaStrategy) PrepareForCreate(ctx context.Context, obj runtime.Object) {
}

func (quotaStrategy) Validate(ctx context.Context, obj runtime.Object) field.ErrorList {
	quota := obj.(*garden.Quota)
	return validation.ValidateQuota(quota)
}

func (quotaStrategy) Canonicalize(obj runtime.Object) {
}

func (quotaStrategy) AllowCreateOnUpdate() bool {
	return false
}

func (quotaStrategy) PrepareForUpdate(ctx context.Context, newObj, oldObj runtime.Object) {
	_ = oldObj.(*garden.Quota)
	_ = newObj.(*garden.Quota)
}

func (quotaStrategy) ValidateUpdate(ctx context.Context, newObj, oldObj runtime.Object) field.ErrorList {
	oldQuota, newQuota := oldObj.(*garden.Quota), newObj.(*garden.Quota)
	return validation.ValidateQuotaUpdate(newQuota, oldQuota)
}

func (quotaStrategy) AllowUnconditionalUpdate() bool {
	return true
}
