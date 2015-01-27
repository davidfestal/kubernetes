/*
Copyright 2014 Google Inc. All rights reserved.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package rest

import (
	"github.com/GoogleCloudPlatform/kubernetes/pkg/api"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/api/errors"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/runtime"
)

type RESTCreateStrategy interface {
	runtime.ObjectTyper
	api.NameGenerator
	Validate(obj runtime.Object) errors.ValidationErrorList
	Reset(obj runtime.Object)
}

// BeforeCreate ensures that common operations for all resources are performed on creation. It only returns
// errors that can be converted to api.Status.
func BeforeCreate(strategy RESTCreateStrategy, ctx api.Context, obj runtime.Object) error {
	_, kind, err := strategy.ObjectVersionAndKind(obj)
	if err != nil {
		return errors.NewInternalError(err)
	}
	objectMeta, err := api.ObjectMetaFor(obj)
	if err != nil {
		return errors.NewInternalError(err)
	}

	if !api.ValidNamespace(ctx, objectMeta) {
		return errors.NewBadRequest("the namespace of the provided object does not match the namespace sent on the request")
	}
	strategy.Reset(obj)
	api.GenerateName(strategy, objectMeta)
	api.FillObjectMetaSystemFields(ctx, objectMeta)

	if errs := strategy.Validate(obj); len(errs) > 0 {
		return errors.NewInvalid(kind, objectMeta.Name, errs)
	}
	return nil
}
